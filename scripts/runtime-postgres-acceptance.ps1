$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$repoRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
$tempBase = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath())
$tempRoot = [System.IO.Path]::GetFullPath((Join-Path $tempBase ("dayorder-postgres-acceptance-" + [guid]::NewGuid().ToString("N"))))
$projectName = "dayorder-acceptance-" + [guid]::NewGuid().ToString("N").Substring(0, 12)
$composeFile = Join-Path $repoRoot "compose.dev.yaml"
$apiProcess = $null
$composeStarted = $false
$checks = 0

function Assert-True {
    param([bool]$Condition, [string]$Message)
    if (-not $Condition) { throw "Acceptance assertion failed: $Message" }
    $script:checks++
    Write-Host "  PASS  $Message"
}

function Get-FreePort {
    $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
    try {
        $listener.Start()
        return ([System.Net.IPEndPoint]$listener.LocalEndpoint).Port
    }
    finally { $listener.Stop() }
}

function Stop-Api {
    if ($null -ne $script:apiProcess -and -not $script:apiProcess.HasExited) {
        Stop-Process -Id $script:apiProcess.Id -Force
        [void]$script:apiProcess.WaitForExit(5000)
    }
    $script:apiProcess = $null
}

function Start-Api {
    param([string]$Binary)
    $script:apiProcess = Start-Process -FilePath $Binary -WorkingDirectory $repoRoot -PassThru -WindowStyle Hidden `
        -RedirectStandardOutput (Join-Path $tempRoot "api.stdout.log") `
        -RedirectStandardError (Join-Path $tempRoot "api.stderr.log")
}

function Wait-Healthy {
    param([string]$BaseUrl)
    $deadline = [DateTime]::UtcNow.AddSeconds(45)
    while ([DateTime]::UtcNow -lt $deadline) {
        try {
            $response = Invoke-WebRequest -UseBasicParsing -Uri "$BaseUrl/api/v1/health" -TimeoutSec 3
            if ($response.StatusCode -eq 200) { return }
        }
        catch { }
        Start-Sleep -Milliseconds 250
    }
    throw "Timed out waiting for PostgreSQL API health"
}

function Invoke-Json {
    param(
        [string]$Method,
        [string]$Uri,
        [Microsoft.PowerShell.Commands.WebRequestSession]$Session,
        $Body = $null,
        [hashtable]$Headers = @{}
    )
    $arguments = @{ UseBasicParsing = $true; Method = $Method; Uri = $Uri; WebSession = $Session; Headers = $Headers; TimeoutSec = 15 }
    if ($null -ne $Body) {
        $arguments.ContentType = "application/json"
        $arguments.Body = $Body | ConvertTo-Json -Depth 30 -Compress
    }
    $response = Invoke-WebRequest @arguments
    $parsed = $null
    if (-not [string]::IsNullOrWhiteSpace($response.Content)) { $parsed = $response.Content | ConvertFrom-Json }
    return [pscustomobject]@{ Status = [int]$response.StatusCode; Body = $parsed }
}

if ($null -eq (Get-Command docker -ErrorAction SilentlyContinue)) {
    Write-Host "SKIPPED: Docker is unavailable; real PostgreSQL runtime acceptance was not run."
    exit 0
}
& docker info *> $null
if ($LASTEXITCODE -ne 0) {
    Write-Host "SKIPPED: Docker daemon is unavailable; real PostgreSQL runtime acceptance was not run."
    exit 0
}

$environmentNames = @(
    "POSTGRES_DB", "POSTGRES_USER", "POSTGRES_PASSWORD", "POSTGRES_PORT",
    "DAYORDER_MIGRATOR_DB_PASSWORD", "DAYORDER_API_DB_PASSWORD", "DAYORDER_WORKER_DB_PASSWORD",
    "MIGRATION_DATABASE_URL", "DATABASE_URL", "DAYORDER_ENV", "DAYORDER_ADDR",
    "DAYORDER_PUBLIC_URL", "DAYORDER_ALLOWED_ORIGINS", "DAYORDER_AUTH_HMAC_KEY"
)
$savedEnvironment = @{}
foreach ($name in $environmentNames) { $savedEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, "Process") }

try {
    New-Item -ItemType Directory -Path $tempRoot | Out-Null
    $postgresPort = Get-FreePort
    $apiPort = Get-FreePort
    $baseUrl = "http://127.0.0.1:$apiPort"
    $env:POSTGRES_DB = "dayorder"
    $env:POSTGRES_USER = "dayorder_admin"
    $env:POSTGRES_PASSWORD = "acceptance-admin-password"
    $env:POSTGRES_PORT = [string]$postgresPort
    $env:DAYORDER_MIGRATOR_DB_PASSWORD = "acceptance-migrator-password"
    $env:DAYORDER_API_DB_PASSWORD = "acceptance-api-password"
    $env:DAYORDER_WORKER_DB_PASSWORD = "acceptance-worker-password"
    $env:MIGRATION_DATABASE_URL = "postgres://dayorder_migrator:acceptance-migrator-password@127.0.0.1:$postgresPort/dayorder?sslmode=disable&search_path=dayorder"
    $env:DATABASE_URL = "postgres://dayorder_api:acceptance-api-password@127.0.0.1:$postgresPort/dayorder?sslmode=disable"
    $env:DAYORDER_ENV = "test"
    $env:DAYORDER_ADDR = "127.0.0.1:$apiPort"
    $env:DAYORDER_PUBLIC_URL = $baseUrl
    $env:DAYORDER_ALLOWED_ORIGINS = $baseUrl
    $env:DAYORDER_AUTH_HMAC_KEY = "acceptance-hmac-key-at-least-thirty-two-bytes"

    Write-Host "Starting isolated PostgreSQL acceptance project $projectName..."
    & docker compose -p $projectName -f $composeFile up -d --wait postgres
    if ($LASTEXITCODE -ne 0) { throw "docker compose failed with exit code $LASTEXITCODE" }
    $composeStarted = $true

    & go run ./apps/api/cmd/migrate
    if ($LASTEXITCODE -ne 0) { throw "database migration failed with exit code $LASTEXITCODE" }
    $binary = Join-Path $tempRoot "dayorder-api.exe"
    & go build -o $binary ./apps/api/cmd/server
    if ($LASTEXITCODE -ne 0) { throw "API build failed with exit code $LASTEXITCODE" }
    Start-Api $binary
    Wait-Healthy $baseUrl

    $session = [Microsoft.PowerShell.Commands.WebRequestSession]::new()
    $email = "acceptance-$([guid]::NewGuid().ToString('N'))@example.com"
    $registered = Invoke-Json "POST" "$baseUrl/api/v1/auth/register" $session @{
        displayName = "Acceptance User"; email = $email; password = "acceptance-password-123"
    }
    Assert-True ($registered.Status -eq 201 -and $registered.Body.verificationRequired) "registration creates a pending verified-email account"

    $tokenSQL = "SELECT event.payload->>'token' FROM dayorder.outbox_events event JOIN dayorder.users usr ON usr.id = event.user_id WHERE usr.email = '$email' AND event.event_type = 'email.verification.requested' ORDER BY event.created_at DESC LIMIT 1;"
    $verificationToken = (& docker compose -p $projectName -f $composeFile exec -T postgres psql -U $env:POSTGRES_USER -d $env:POSTGRES_DB -Atc $tokenSQL | Out-String).Trim()
    Assert-True (-not [string]::IsNullOrWhiteSpace($verificationToken)) "verification token is transactionally queued"
    $verified = Invoke-Json "POST" "$baseUrl/api/v1/auth/verify-email" $session @{ token = $verificationToken }
    Assert-True ($verified.Status -eq 200 -and $verified.Body.user.status -eq "active") "email verification activates the account and establishes a session"

    $deviceA = [guid]::NewGuid().ToString()
    $deviceB = [guid]::NewGuid().ToString()
    $deviceAResult = Invoke-Json "PUT" "$baseUrl/api/v1/users/me/devices/$deviceA" $session @{ deviceName = "Acceptance A"; platform = "web" }
    $deviceBResult = Invoke-Json "PUT" "$baseUrl/api/v1/users/me/devices/$deviceB" $session @{ deviceName = "Acceptance B"; platform = "web" }
    Assert-True ($deviceAResult.Status -eq 201 -and $deviceBResult.Status -eq 201) "two devices register for the same personal account"
    $bootstrap = Invoke-Json "GET" "$baseUrl/api/v1/sync/bootstrap" $session $null @{ "X-Device-ID" = $deviceB }

    $goalHeaders = @{ "X-Device-ID" = $deviceA; "Idempotency-Key" = [guid]::NewGuid().ToString() }
    $createdGoal = Invoke-Json "POST" "$baseUrl/api/v1/goals" $session @{
        title = "PostgreSQL acceptance goal"; why = "runtime persistence"; area = "Work"; metricType = "project";
        targetValue = 1; currentValue = 0; unit = "item"; startDate = "2026-08-28"; status = "active"; health = "normal"
    } $goalHeaders
    Assert-True ($createdGoal.Status -eq 201 -and $createdGoal.Body.version -eq 1) "core resource API creates a versioned goal"

    $encodedCursor = [uri]::EscapeDataString([string]$bootstrap.Body.cursor)
    $changes = Invoke-Json "GET" "$baseUrl/api/v1/sync/changes?cursor=$encodedCursor&limit=500" $session $null @{ "X-Device-ID" = $deviceB }
    $goalChange = @($changes.Body.changes | Where-Object { $_.entityType -eq "goal" -and $_.entityId -eq $createdGoal.Body.id })
    Assert-True ($goalChange.Count -eq 1) "the second device receives the first device's incremental change"

    Stop-Api
    Start-Api $binary
    Wait-Healthy $baseUrl
    $loadedGoal = Invoke-Json "GET" "$baseUrl/api/v1/goals/$($createdGoal.Body.id)" $session
    Assert-True ($loadedGoal.Status -eq 200 -and $loadedGoal.Body.title -eq "PostgreSQL acceptance goal") "session and resource survive an API restart"

    Write-Host "PostgreSQL runtime acceptance passed: $checks checks."
}
finally {
    Stop-Api
    if ($composeStarted -and $projectName -like "dayorder-acceptance-*") {
        & docker compose -p $projectName -f $composeFile down -v --remove-orphans | Out-Null
    }
    foreach ($name in $savedEnvironment.Keys) {
        [Environment]::SetEnvironmentVariable($name, $savedEnvironment[$name], "Process")
    }
    if (Test-Path -LiteralPath $tempRoot) {
        $resolved = [System.IO.Path]::GetFullPath($tempRoot)
        if ($resolved.StartsWith($tempBase, [System.StringComparison]::OrdinalIgnoreCase) -and [System.IO.Path]::GetFileName($resolved) -like "dayorder-postgres-acceptance-*") {
            Remove-Item -LiteralPath $resolved -Recurse -Force
        }
    }
}
