[CmdletBinding()]
param([switch]$RequireDocker)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$repoRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
$tempBase = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath())
$tempRoot = [System.IO.Path]::GetFullPath((Join-Path $tempBase ("dayorder-postgres-acceptance-" + [guid]::NewGuid().ToString("N"))))
$projectName = "dayorder-acceptance-" + [guid]::NewGuid().ToString("N").Substring(0, 12)
$composeFile = Join-Path $repoRoot "compose.dev.yaml"
$apiProcess = $null
$workerProcess = $null
$composeStarted = $false
$checks = 0

function Assert-True {
    param([bool]$Condition, [string]$Message)
    if (-not $Condition) { throw "Acceptance assertion failed: $Message" }
    $script:checks++
    Write-Host "  PASS  $Message"
}

function Assert-Status {
    param($Response, [int]$Expected, [string]$Message)
    Assert-True ([int]$Response.Status -eq $Expected) "$Message (HTTP $Expected)"
}

function Get-FreePort {
    $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
    try {
        $listener.Start()
        return ([System.Net.IPEndPoint]$listener.LocalEndpoint).Port
    }
    finally { $listener.Stop() }
}

function Stop-ProcessSafe {
    param($Process)
    if ($null -ne $Process -and -not $Process.HasExited) {
        Stop-Process -Id $Process.Id -Force
        [void]$Process.WaitForExit(5000)
    }
}

function Stop-Api { Stop-ProcessSafe $script:apiProcess; $script:apiProcess = $null }
function Stop-Worker { Stop-ProcessSafe $script:workerProcess; $script:workerProcess = $null }

function Write-ProcessLogs {
    param([string]$LogRoot)
    foreach ($fileName in @("api.stdout.log", "api.stderr.log", "worker.stdout.log", "worker.stderr.log")) {
        $path = Join-Path $LogRoot $fileName
        if (-not (Test-Path -LiteralPath $path)) { continue }
        Write-Host "::group::Runtime process log: $fileName"
        $content = Get-Content -LiteralPath $path -Raw
        if ([string]::IsNullOrWhiteSpace($content)) {
            Write-Host "<empty>"
        }
        else {
            Write-Host $content.TrimEnd()
        }
        Write-Host "::endgroup::"
    }
}

function Start-Api {
    param([string]$Binary)
    $arguments = @{
        FilePath = $Binary; WorkingDirectory = $repoRoot; PassThru = $true
        RedirectStandardOutput = (Join-Path $tempRoot "api.stdout.log")
        RedirectStandardError = (Join-Path $tempRoot "api.stderr.log")
    }
    if ($env:OS -eq "Windows_NT") { $arguments.WindowStyle = "Hidden" }
    $script:apiProcess = Start-Process @arguments
}

function Start-Worker {
    param([string]$Binary)
    $arguments = @{
        FilePath = $Binary; WorkingDirectory = $repoRoot; PassThru = $true
        RedirectStandardOutput = (Join-Path $tempRoot "worker.stdout.log")
        RedirectStandardError = (Join-Path $tempRoot "worker.stderr.log")
    }
    if ($env:OS -eq "Windows_NT") { $arguments.WindowStyle = "Hidden" }
    $script:workerProcess = Start-Process @arguments
}

function Wait-Url {
    param([string]$Uri, [int]$TimeoutSeconds = 45)
    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
    while ([DateTime]::UtcNow -lt $deadline) {
        try {
            $response = Invoke-WebRequest -UseBasicParsing -Uri $Uri -TimeoutSec 3
            if ($response.StatusCode -eq 200) { return }
        }
        catch { }
        Start-Sleep -Milliseconds 250
    }
    throw "Timed out waiting for $Uri"
}

function Invoke-Json {
    param(
        [string]$Method,
        [string]$Uri,
        [Microsoft.PowerShell.Commands.WebRequestSession]$Session,
        $Body = $null,
        [hashtable]$Headers = @{},
        [string]$ContentType = "application/json"
    )
    $arguments = @{ UseBasicParsing = $true; Method = $Method; Uri = $Uri; WebSession = $Session; Headers = $Headers; TimeoutSec = 20 }
    if ($null -ne $Body) {
        $arguments.ContentType = $ContentType
        $arguments.Body = $Body | ConvertTo-Json -Depth 30 -Compress
    }
    try {
        $response = Invoke-WebRequest @arguments
        $content = $response.Content
        $status = [int]$response.StatusCode
        $responseHeaders = $response.Headers
    }
    catch {
        $responseProperty = $_.Exception.PSObject.Properties["Response"]
        if ($null -eq $responseProperty -or $null -eq $responseProperty.Value) { throw }
        $errorResponse = $responseProperty.Value
        $status = [int]$errorResponse.StatusCode
        $responseHeaders = $errorResponse.Headers
        $content = [string]$_.ErrorDetails.Message
    }
    $parsed = $null
    if (-not [string]::IsNullOrWhiteSpace($content)) {
        try { $parsed = $content | ConvertFrom-Json } catch { $parsed = $content }
    }
    return [pscustomobject]@{ Status = $status; Body = $parsed; Headers = $responseHeaders }
}

function New-MutationHeaders {
    param([string]$DeviceID, [int]$Version = 0)
    $headers = @{ "X-Device-ID" = $DeviceID; "Idempotency-Key" = [guid]::NewGuid().ToString() }
    if ($Version -gt 0) { $headers["If-Match"] = [string]$Version }
    return $headers
}

function Invoke-AdminSQL {
    param([string]$SQL)
    $output = & docker compose -p $projectName -f $composeFile exec -T postgres psql -v ON_ERROR_STOP=1 -U $env:POSTGRES_USER -d $env:POSTGRES_DB -Atc $SQL
    if ($LASTEXITCODE -ne 0) { throw "PostgreSQL assertion query failed" }
    return ($output | Out-String).Trim()
}

function Invoke-APISQL {
    param([string]$SQL)
    $output = & docker compose -p $projectName -f $composeFile exec -T -e PGPASSWORD=acceptance-api-password postgres `
        psql -v ON_ERROR_STOP=1 -h 127.0.0.1 -U dayorder_api -d $env:POSTGRES_DB -Atc $SQL
    if ($LASTEXITCODE -ne 0) { throw "restricted PostgreSQL assertion query failed" }
    return ($output | Out-String).Trim()
}

function Get-AccountToken {
    param([string]$Email, [string]$EventType)
    $safeEmail = $Email.Replace("'", "''")
    return Invoke-AdminSQL "SELECT event.payload->>'token' FROM dayorder.outbox_events event JOIN dayorder.users usr ON usr.id = event.user_id WHERE usr.email = '$safeEmail' AND event.event_type = '$EventType' ORDER BY event.created_at DESC LIMIT 1;"
}

function Register-VerifiedAccount {
    param([string]$BaseUrl, [string]$Email, [string]$DisplayName, [string]$Password)
    $session = [Microsoft.PowerShell.Commands.WebRequestSession]::new()
    $registered = Invoke-Json "POST" "$BaseUrl/api/v1/auth/register" $session @{ displayName = $DisplayName; email = $Email; password = $Password }
    Assert-Status $registered 201 "$DisplayName registration creates a pending account"
    Assert-True ([bool]$registered.Body.verificationRequired) "$DisplayName requires email verification"
    $token = Get-AccountToken $Email "email.verification.requested"
    Assert-True (-not [string]::IsNullOrWhiteSpace($token)) "$DisplayName verification token is queued transactionally"
    $verified = Invoke-Json "POST" "$BaseUrl/api/v1/auth/verify-email" $session @{ token = $token }
    Assert-Status $verified 200 "$DisplayName verification activates the account"
    return [pscustomobject]@{ Session = $session; User = $verified.Body.user; Password = $Password }
}

$dockerAvailable = $null -ne (Get-Command docker -ErrorAction SilentlyContinue)
if ($dockerAvailable) {
    & docker info *> $null
    $dockerAvailable = $LASTEXITCODE -eq 0
}
if (-not $dockerAvailable) {
    if ($RequireDocker -or $env:CI) { throw "Docker is required for PostgreSQL runtime acceptance" }
    Write-Host "SKIPPED: Docker is unavailable; real PostgreSQL runtime acceptance was not run."
    return
}

$environmentNames = @(
    "POSTGRES_DB", "POSTGRES_USER", "POSTGRES_PASSWORD", "POSTGRES_PORT",
    "DAYORDER_MIGRATOR_DB_PASSWORD", "DAYORDER_API_DB_PASSWORD", "DAYORDER_WORKER_DB_PASSWORD",
    "MIGRATION_DATABASE_URL", "DATABASE_URL", "WORKER_DATABASE_URL", "DAYORDER_ENV", "DAYORDER_ADDR",
    "DAYORDER_PUBLIC_URL", "DAYORDER_ALLOWED_ORIGINS", "DAYORDER_AUTH_HMAC_KEY",
    "DAYORDER_WORKER_METRICS_ADDR", "DAYORDER_WORKER_POLL_RATE", "DAYORDER_MAIL_SINK", "DAYORDER_AGENT_PROVIDER"
)
$savedEnvironment = @{}
foreach ($name in $environmentNames) { $savedEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, "Process") }

try {
    New-Item -ItemType Directory -Path $tempRoot | Out-Null
    $postgresPort = Get-FreePort
    $apiPort = Get-FreePort
    $workerPort = Get-FreePort
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
    $env:WORKER_DATABASE_URL = "postgres://dayorder_worker:acceptance-worker-password@127.0.0.1:$postgresPort/dayorder?sslmode=disable"
    $env:DAYORDER_ENV = "test"
    $env:DAYORDER_ADDR = "127.0.0.1:$apiPort"
    $env:DAYORDER_PUBLIC_URL = $baseUrl
    $env:DAYORDER_ALLOWED_ORIGINS = $baseUrl
    $env:DAYORDER_AUTH_HMAC_KEY = "acceptance-hmac-key-at-least-thirty-two-bytes"
    $env:DAYORDER_WORKER_METRICS_ADDR = "127.0.0.1:$workerPort"
    $env:DAYORDER_WORKER_POLL_RATE = "100ms"
    $env:DAYORDER_MAIL_SINK = "log"
    $env:DAYORDER_AGENT_PROVIDER = "deterministic"

    Write-Host "Starting isolated PostgreSQL acceptance project $projectName..."
    & docker compose -p $projectName -f $composeFile up -d --wait postgres
    if ($LASTEXITCODE -ne 0) { throw "docker compose failed with exit code $LASTEXITCODE" }
    $composeStarted = $true

    & go run ./apps/api/cmd/migrate
    if ($LASTEXITCODE -ne 0) { throw "empty-database migration failed" }
    Assert-True ((Invoke-AdminSQL "SELECT dirty = false FROM dayorder.schema_migrations;") -eq "t") "empty PostgreSQL database migrates to a clean schema"

    $apiBinary = Join-Path $tempRoot "dayorder-api.exe"
    $workerBinary = Join-Path $tempRoot "dayorder-worker.exe"
    & go build -o $apiBinary ./apps/api/cmd/server
    if ($LASTEXITCODE -ne 0) { throw "API build failed" }
    & go build -o $workerBinary ./apps/api/cmd/worker
    if ($LASTEXITCODE -ne 0) { throw "Worker build failed" }
    Start-Api $apiBinary
    Wait-Url "$baseUrl/health/ready"
    Assert-True $true "API readiness verifies PostgreSQL connectivity"

    $anonymous = [Microsoft.PowerShell.Commands.WebRequestSession]::new()
    Assert-Status (Invoke-Json "GET" "$baseUrl/api/v1/goals" $anonymous) 401 "anonymous resource reads are rejected"

    $suffix = [guid]::NewGuid().ToString("N")
    $accountA = Register-VerifiedAccount $baseUrl "acceptance-a-$suffix@example.com" "Acceptance A" "acceptance-password-123"
    $accountB = Register-VerifiedAccount $baseUrl "acceptance-b-$suffix@example.com" "Acceptance B" "acceptance-password-456"
    $deviceA = [guid]::NewGuid().ToString()
    $deviceASecond = [guid]::NewGuid().ToString()
    $deviceB = [guid]::NewGuid().ToString()
    Assert-Status (Invoke-Json "PUT" "$baseUrl/api/v1/users/me/devices/$deviceA" $accountA.Session @{ deviceName = "A primary"; platform = "web" }) 201 "account A registers its primary device"
    Assert-Status (Invoke-Json "PUT" "$baseUrl/api/v1/users/me/devices/$deviceASecond" $accountA.Session @{ deviceName = "A secondary"; platform = "web" }) 201 "account A registers a second device"
    Assert-Status (Invoke-Json "PUT" "$baseUrl/api/v1/users/me/devices/$deviceB" $accountB.Session @{ deviceName = "B primary"; platform = "web" }) 201 "account B registers an isolated device"
    $bootstrap = Invoke-Json "GET" "$baseUrl/api/v1/sync/bootstrap" $accountA.Session $null @{ "X-Device-ID" = $deviceASecond }
    Assert-Status $bootstrap 200 "second device bootstraps a signed sync cursor"

    $goalBody = @{ title = "PostgreSQL acceptance goal"; why = "runtime persistence"; area = "Work"; metricType = "project"; targetValue = 10; currentValue = 1; unit = "item"; startDate = "2026-08-28"; status = "active"; health = "normal" }
    $goalHeaders = New-MutationHeaders $deviceA
    $createdGoal = Invoke-Json "POST" "$baseUrl/api/v1/goals" $accountA.Session $goalBody $goalHeaders
    Assert-Status $createdGoal 201 "goal create succeeds"
    $replayedGoal = Invoke-Json "POST" "$baseUrl/api/v1/goals" $accountA.Session $goalBody $goalHeaders
    Assert-True ($replayedGoal.Status -eq 201 -and $replayedGoal.Body.id -eq $createdGoal.Body.id) "resource create retries are idempotent"
    Assert-Status (Invoke-Json "GET" "$baseUrl/api/v1/goals/$($createdGoal.Body.id)" $accountB.Session) 404 "account B cannot read account A resources"

    $accountBGoalBody = $goalBody.Clone()
    $accountBGoalBody.title = "Account B RLS goal"
    $accountBGoal = Invoke-Json "POST" "$baseUrl/api/v1/goals" $accountB.Session $accountBGoalBody (New-MutationHeaders $deviceB)
    Assert-Status $accountBGoal 201 "account B creates its own RLS control row"
    $rlsSQL = "SELECT set_config('dayorder.user_id', '$($accountB.User.id)', false); SELECT (SELECT count(*) FROM dayorder.goals WHERE id = '$($createdGoal.Body.id)')::text || '|' || (SELECT count(*) FROM dayorder.goals WHERE id = '$($accountBGoal.Body.id)')::text;"
    $rlsLines = @((Invoke-APISQL $rlsSQL) -split "`r?`n")
    Assert-True ($rlsLines[-1] -eq "0|1") "PostgreSQL RLS hides account A rows while preserving account B access"

    $milestone = Invoke-Json "POST" "$baseUrl/api/v1/goals/$($createdGoal.Body.id)/milestones" $accountA.Session @{ title = "Acceptance milestone"; sortOrder = 1 } (New-MutationHeaders $deviceA)
    Assert-Status $milestone 201 "milestone create succeeds"
    $milestone = Invoke-Json "PATCH" "$baseUrl/api/v1/milestones/$($milestone.Body.id)" $accountA.Session @{ title = "Updated milestone" } (New-MutationHeaders $deviceA $milestone.Body.version) "application/merge-patch+json"
    Assert-Status $milestone 200 "milestone update succeeds"
    Assert-Status (Invoke-Json "DELETE" "$baseUrl/api/v1/milestones/$($milestone.Body.id)" $accountA.Session $null (New-MutationHeaders $deviceA $milestone.Body.version)) 204 "milestone delete succeeds"

    $task = Invoke-Json "POST" "$baseUrl/api/v1/tasks" $accountA.Session @{ title = "Acceptance task"; status = "todo"; priority = "important"; estimateMinutes = 30; goalId = $createdGoal.Body.id } (New-MutationHeaders $deviceA)
    Assert-Status $task 201 "task create succeeds"
    $updatedTask = Invoke-Json "PATCH" "$baseUrl/api/v1/tasks/$($task.Body.id)" $accountA.Session @{ status = "doing" } (New-MutationHeaders $deviceA $task.Body.version) "application/merge-patch+json"
    Assert-Status $updatedTask 200 "task update succeeds"
    $staleTask = Invoke-Json "PATCH" "$baseUrl/api/v1/tasks/$($task.Body.id)" $accountA.Session @{ status = "done" } (New-MutationHeaders $deviceASecond $task.Body.version) "application/merge-patch+json"
    Assert-Status $staleTask 409 "stale entity version produces a conflict"

    $event = Invoke-Json "POST" "$baseUrl/api/v1/calendar-events" $accountA.Session @{ title = "Acceptance event"; startAt = "2026-08-28T09:00:00Z"; endAt = "2026-08-28T10:00:00Z"; timezone = "UTC"; kind = "fixed"; goalId = $createdGoal.Body.id; reminders = @(@{ offsetMinutes = 10; channel = "in_app" }) } (New-MutationHeaders $deviceA)
    Assert-Status $event 201 "calendar event and reminder create succeeds"
    $event = Invoke-Json "PATCH" "$baseUrl/api/v1/calendar-events/$($event.Body.event.id)" $accountA.Session @{ title = "Updated event" } (New-MutationHeaders $deviceA $event.Body.event.version) "application/merge-patch+json"
    Assert-Status $event 200 "calendar event update succeeds"

    $record = Invoke-Json "POST" "$baseUrl/api/v1/records" $accountA.Session @{ rawText = "Acceptance record"; kind = "status"; occurredAt = "2026-08-28T08:00:00Z"; energy = 4; tags = @("acceptance") } (New-MutationHeaders $deviceA)
    Assert-Status $record 201 "record create succeeds"
    $record = Invoke-Json "PATCH" "$baseUrl/api/v1/records/$($record.Body.id)" $accountA.Session @{ rawText = "Updated record" } (New-MutationHeaders $deviceA $record.Body.version) "application/merge-patch+json"
    Assert-Status $record 200 "record update succeeds"

    $note = Invoke-Json "POST" "$baseUrl/api/v1/notes" $accountA.Session @{ title = "Acceptance note"; bodyMarkdown = "Runtime body"; category = "Other"; tags = @("acceptance"); linkedEntityIds = @($createdGoal.Body.id) } (New-MutationHeaders $deviceA)
    Assert-Status $note 201 "note create succeeds"
    $note = Invoke-Json "PATCH" "$baseUrl/api/v1/notes/$($note.Body.id)" $accountA.Session @{ bodyMarkdown = "Updated runtime body" } (New-MutationHeaders $deviceA $note.Body.version) "application/merge-patch+json"
    Assert-Status $note 200 "note update succeeds"

    $review = Invoke-Json "POST" "$baseUrl/api/v1/daily-reviews" $accountA.Session @{ reviewDate = "2026-08-28"; wins = "Acceptance passed"; blockers = ""; mood = 4; energy = 4; tomorrowFocus = "Keep shipping" } (New-MutationHeaders $deviceA)
    Assert-Status $review 201 "daily review create succeeds"
    $review = Invoke-Json "PATCH" "$baseUrl/api/v1/daily-reviews/$($review.Body.id)" $accountA.Session @{ wins = "Acceptance updated" } (New-MutationHeaders $deviceA $review.Body.version) "application/merge-patch+json"
    Assert-Status $review 200 "daily review update succeeds"

    $settings = Invoke-Json "GET" "$baseUrl/api/v1/users/me/settings" $accountA.Session
    $settings = Invoke-Json "PATCH" "$baseUrl/api/v1/users/me/settings" $accountA.Session @{ energy = 5 } (New-MutationHeaders $deviceA $settings.Body.version) "application/merge-patch+json"
    Assert-True ($settings.Status -eq 200 -and $settings.Body.settings.energy -eq 5) "versioned user settings update succeeds"

    $encodedCursor = [uri]::EscapeDataString([string]$bootstrap.Body.cursor)
    $changes = Invoke-Json "GET" "$baseUrl/api/v1/sync/changes?cursor=$encodedCursor&limit=500" $accountA.Session $null @{ "X-Device-ID" = $deviceASecond }
    Assert-True ($changes.Status -eq 200 -and @($changes.Body.changes).Count -gt 0) "second device receives incremental entity changes"
    $invalidCursor = Invoke-Json "GET" "$baseUrl/api/v1/sync/changes?cursor=invalid&limit=10" $accountA.Session $null @{ "X-Device-ID" = $deviceASecond }
    Assert-Status $invalidCursor 400 "invalid sync cursors require a safe client rebuild"

    foreach ($resource in @(
        @{ Path = "tasks/$($task.Body.id)"; Version = $updatedTask.Body.version },
        @{ Path = "calendar-events/$($event.Body.event.id)"; Version = $event.Body.event.version },
        @{ Path = "records/$($record.Body.id)"; Version = $record.Body.version },
        @{ Path = "notes/$($note.Body.id)"; Version = $note.Body.version },
        @{ Path = "daily-reviews/$($review.Body.id)"; Version = $review.Body.version }
    )) {
        Assert-Status (Invoke-Json "DELETE" "$baseUrl/api/v1/$($resource.Path)" $accountA.Session $null (New-MutationHeaders $deviceA $resource.Version)) 204 "$($resource.Path) delete succeeds"
    }

    $resetRequested = Invoke-Json "POST" "$baseUrl/api/v1/auth/password-reset/request" $anonymous @{ email = "acceptance-a-$suffix@example.com" }
    Assert-Status $resetRequested 202 "password reset request is accepted without account disclosure"
    $resetToken = Get-AccountToken "acceptance-a-$suffix@example.com" "email.password_reset.requested"
    Assert-True (-not [string]::IsNullOrWhiteSpace($resetToken)) "password reset token is queued transactionally"
    Assert-Status (Invoke-Json "POST" "$baseUrl/api/v1/auth/password-reset/complete" $anonymous @{ token = $resetToken; password = "acceptance-password-789" }) 204 "password reset completes"
    Assert-Status (Invoke-Json "GET" "$baseUrl/api/v1/auth/session" $accountA.Session) 401 "password reset revokes prior sessions"
    $accountA.Session = [Microsoft.PowerShell.Commands.WebRequestSession]::new()
    Assert-Status (Invoke-Json "POST" "$baseUrl/api/v1/auth/login" $accountA.Session @{ email = "acceptance-a-$suffix@example.com"; password = "acceptance-password-789" }) 200 "new password establishes a rotated session"

    Stop-Api
    Start-Api $apiBinary
    Wait-Url "$baseUrl/health/ready"
    $loadedGoal = Invoke-Json "GET" "$baseUrl/api/v1/goals/$($createdGoal.Body.id)" $accountA.Session
    Assert-True ($loadedGoal.Status -eq 200 -and $loadedGoal.Body.title -eq "PostgreSQL acceptance goal") "session and resources survive an API restart"

    & node (Join-Path $repoRoot "scripts\load-smoke.js") --base-url $baseUrl --email "acceptance-a-$suffix@example.com" --password "acceptance-password-789" --cycles 5 --concurrency 2 --p95-ms 2000
    if ($LASTEXITCODE -ne 0) { throw "load smoke failed" }
    Assert-True $true "concurrent resource CRUD load smoke stays within its error and latency budget"

    Start-Worker $workerBinary
    Wait-Url "http://127.0.0.1:$workerPort/metrics"
    $deadline = [DateTime]::UtcNow.AddSeconds(20)
    $processed = 0
    do {
        $processed = [int](Invoke-AdminSQL "SELECT count(*) FROM dayorder.outbox_events WHERE status = 'processed';")
        if ($processed -gt 0) { break }
        Start-Sleep -Milliseconds 250
    } while ([DateTime]::UtcNow -lt $deadline)
    Assert-True ($processed -gt 0) "Worker drains transactional Outbox events"
    Stop-Worker
    Start-Worker $workerBinary
    Wait-Url "http://127.0.0.1:$workerPort/metrics"
    Assert-True $true "Worker restarts cleanly against the persistent queue"

    Write-Host "PostgreSQL runtime acceptance passed: $checks checks."
}
catch {
    $failure = $_
    Write-Host "PostgreSQL runtime acceptance failed: $($failure.Exception.Message)"
    try {
        Write-ProcessLogs $tempRoot
    }
    catch {
        Write-Warning "Failed to emit runtime process logs: $($_.Exception.Message)"
    }
    throw $failure
}
finally {
    Stop-Worker
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
