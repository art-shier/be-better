[CmdletBinding()]
param(
    [string]$BaseUrl,
    [string]$Email,
    [string]$Password,
    [string]$ComposeFile,
    [string]$EnvironmentFile,
    [string]$ProjectName,
    [switch]$Insecure
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest
$checks = 0
if (-not $ComposeFile) { $ComposeFile = Join-Path $PSScriptRoot "..\deploy\compose.yaml" }

function Assert-True {
    param([bool]$Condition, [string]$Message)
    if (-not $Condition) { throw "Security acceptance failed: $Message" }
    $script:checks++
    Write-Host "  PASS  $Message"
}

function Invoke-CheckedWebRequest {
    param([string]$Method, [string]$Uri, $Body = $null, $Session = $null, [hashtable]$Headers = @{})
    $arguments = @{ UseBasicParsing = $true; Method = $Method; Uri = $Uri; Headers = $Headers; TimeoutSec = 15 }
    if ($null -ne $Session) { $arguments.WebSession = $Session }
    if ($null -ne $Body) {
        $arguments.ContentType = "application/json"
        $arguments.Body = $Body | ConvertTo-Json -Depth 20 -Compress
    }
    if ($Insecure -and (Get-Command Invoke-WebRequest).Parameters.ContainsKey("SkipCertificateCheck")) {
        $arguments.SkipCertificateCheck = $true
    }
    try {
        return Invoke-WebRequest @arguments
    }
    catch {
        if ($null -eq $_.Exception.Response) { throw }
        $responseHeaders = @{}
        foreach ($header in $_.Exception.Response.Headers) { $responseHeaders[$header.Key] = ($header.Value -join ", ") }
        if ($null -ne $_.Exception.Response.Content) {
            foreach ($header in $_.Exception.Response.Content.Headers) { $responseHeaders[$header.Key] = ($header.Value -join ", ") }
        }
        return [pscustomobject]@{
            StatusCode = [int]$_.Exception.Response.StatusCode
            Headers = $responseHeaders
            Content = [string]$_.ErrorDetails.Message
        }
    }
}

$repoRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
& node (Join-Path $PSScriptRoot "validate-deploy.mjs")
if ($LASTEXITCODE -ne 0) { throw "static deployment validation failed" }
Assert-True $true "static Compose, image, TLS, secret and PostgreSQL hardening rules pass"

$composeText = Get-Content -Encoding utf8 -LiteralPath $ComposeFile -Raw
$postgresBlock = [regex]::Match($composeText, "(?ms)^  postgres:\r?\n(?:(?!^  [a-zA-Z0-9_-]+:).)*").Value
Assert-True ($composeText -match "(?m)^\s{2}data:\r?\n\s{4}internal:\s*true") "the PostgreSQL data network is internal"
Assert-True ($postgresBlock -and $postgresBlock -notmatch "(?m)^\s{4}ports:") "PostgreSQL has no published host port"
Assert-True ($composeText -notmatch "DAYORDER_AGENT_") "the disabled Agent has no production provider configuration"

if ($EnvironmentFile -and $null -ne (Get-Command docker -ErrorAction SilentlyContinue)) {
    $configuration = & docker compose --env-file $EnvironmentFile -f $ComposeFile config --format json 2>&1
    if ($LASTEXITCODE -ne 0) { throw "docker compose config failed: $($configuration -join [Environment]::NewLine)" }
    $model = ($configuration -join [Environment]::NewLine) | ConvertFrom-Json
    Assert-True ($null -eq $model.services.postgres.PSObject.Properties["ports"]) "rendered Compose does not publish PostgreSQL"
    Assert-True ([bool]$model.networks.data.internal) "rendered Compose preserves the internal data network"
}

if ($ProjectName -and $null -ne (Get-Command docker -ErrorAction SilentlyContinue)) {
    $composeArguments = @("compose", "-p", $ProjectName)
    if ($EnvironmentFile) { $composeArguments += @("--env-file", $EnvironmentFile) }
    $composeArguments += @("-f", $ComposeFile)
    foreach ($service in @("api", "worker", "postgres", "caddy")) {
        $containerId = (& docker @($composeArguments + @("ps", "-q", $service)) | Out-String).Trim()
        Assert-True (-not [string]::IsNullOrWhiteSpace($containerId)) "$service container is running"
        $inspection = (& docker inspect $containerId | ConvertFrom-Json)[0]
        Assert-True ([bool]$inspection.HostConfig.ReadonlyRootfs) "$service uses a read-only root filesystem"
        Assert-True ($inspection.Config.User -notin @("", "0", "root")) "$service runs as a non-root user"
        Assert-True (@($inspection.HostConfig.CapDrop) -contains "ALL") "$service drops all Linux capabilities"
    }
}

if ($BaseUrl) {
    if ($Insecure -and -not (Get-Command Invoke-WebRequest).Parameters.ContainsKey("SkipCertificateCheck")) {
        [System.Net.ServicePointManager]::ServerCertificateValidationCallback = { $true }
    }
    $origin = $BaseUrl.TrimEnd("/")
    Assert-True ($origin -match "^https://") "live acceptance uses HTTPS"
    $ready = Invoke-CheckedWebRequest "GET" "$origin/health/ready"
    Assert-True ([int]$ready.StatusCode -eq 200) "Caddy proxies readiness over TLS"
    $deepLink = Invoke-CheckedWebRequest "GET" "$origin/goals/acceptance-deep-link"
    Assert-True ([int]$deepLink.StatusCode -eq 200 -and $deepLink.Content -match '<div id="root">') "SPA deep links return the application shell"
    $anonymous = Invoke-CheckedWebRequest "GET" "$origin/api/v1/goals"
    Assert-True ([int]$anonymous.StatusCode -eq 401) "anonymous resource access is rejected at the API"
    Assert-True ([string]$anonymous.Headers["Strict-Transport-Security"] -match "max-age=31536000") "HSTS reaches API responses"
    Assert-True ([string]$anonymous.Headers["Content-Security-Policy"] -match "default-src 'self'") "CSP reaches API responses"
    $foreignOrigin = Invoke-CheckedWebRequest "POST" "$origin/api/v1/auth/login" @{ email = "nobody@example.com"; password = "invalid-password" } $null @{ Origin = "https://attacker.example" }
    Assert-True ([int]$foreignOrigin.StatusCode -eq 403) "cross-origin credential requests are rejected"

    if ($Email -and $Password) {
        $session = [Microsoft.PowerShell.Commands.WebRequestSession]::new()
        $login = Invoke-CheckedWebRequest "POST" "$origin/api/v1/auth/login" @{ email = $Email; password = $Password } $session
        Assert-True ([int]$login.StatusCode -eq 200) "security acceptance account can log in"
        $setCookie = [string]$login.Headers["Set-Cookie"]
        Assert-True ($setCookie -match "(?i)HttpOnly" -and $setCookie -match "(?i)Secure" -and $setCookie -match "(?i)SameSite=Lax") "session cookie is Secure, HttpOnly and SameSite=Lax"
    }
}

Write-Host "Security acceptance passed: $checks checks."
