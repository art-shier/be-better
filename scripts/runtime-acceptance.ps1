$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

Add-Type -AssemblyName System.Net.Http

$repoRoot = [System.IO.Path]::GetFullPath((Join-Path $PSScriptRoot ".."))
$tempBase = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath())
$tempRoot = [System.IO.Path]::GetFullPath((Join-Path $tempBase ("dayorder-runtime-" + [guid]::NewGuid().ToString("N"))))
$apiProcess = $null
$viteProcess = $null
$clients = New-Object System.Collections.Generic.List[object]
$checks = 0

function Assert-True {
    param(
        [Parameter(Mandatory = $true)][bool]$Condition,
        [Parameter(Mandatory = $true)][string]$Message
    )
    if (-not $Condition) {
        throw "Acceptance assertion failed: $Message"
    }
    $script:checks++
    Write-Host "  PASS  $Message"
}

function Assert-Status {
    param(
        [Parameter(Mandatory = $true)]$Response,
        [Parameter(Mandatory = $true)][int]$Expected,
        [Parameter(Mandatory = $true)][string]$Message
    )
    Assert-True ($Response.Status -eq $Expected) "$Message (HTTP $Expected)"
    if ($Response.Status -ne $Expected) {
        Write-Host $Response.Body
    }
}

function Get-FreePort {
    $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
    try {
        $listener.Start()
        return ([System.Net.IPEndPoint]$listener.LocalEndpoint).Port
    }
    finally {
        $listener.Stop()
    }
}

function New-DayOrderClient {
    $handler = [System.Net.Http.HttpClientHandler]::new()
    $handler.UseCookies = $true
    $handler.CookieContainer = [System.Net.CookieContainer]::new()
    $handler.AllowAutoRedirect = $false
    $client = [System.Net.Http.HttpClient]::new($handler)
    $client.Timeout = [TimeSpan]::FromSeconds(15)
    $record = [pscustomobject]@{ Client = $client; Handler = $handler }
    $script:clients.Add($record)
    return $record
}

function Invoke-DayOrder {
    param(
        [Parameter(Mandatory = $true)]$ClientRecord,
        [Parameter(Mandatory = $true)][string]$Method,
        [Parameter(Mandatory = $true)][string]$Uri,
        $Body = $null,
        [hashtable]$Headers = @{}
    )
    $request = [System.Net.Http.HttpRequestMessage]::new([System.Net.Http.HttpMethod]::new($Method), $Uri)
    try {
        foreach ($name in $Headers.Keys) {
            [void]$request.Headers.TryAddWithoutValidation($name, [string]$Headers[$name])
        }
        if ($null -ne $Body) {
            $json = $Body | ConvertTo-Json -Depth 50 -Compress
            $request.Content = [System.Net.Http.StringContent]::new($json, [System.Text.Encoding]::UTF8, "application/json")
        }
        $response = $ClientRecord.Client.SendAsync($request).GetAwaiter().GetResult()
        try {
            $content = $response.Content.ReadAsStringAsync().GetAwaiter().GetResult()
            $responseHeaders = @{}
            foreach ($header in $response.Headers) {
                $responseHeaders[$header.Key] = ($header.Value -join ", ")
            }
            foreach ($header in $response.Content.Headers) {
                $responseHeaders[$header.Key] = ($header.Value -join ", ")
            }
            return [pscustomobject]@{
                Status = [int]$response.StatusCode
                Body = $content
                Headers = $responseHeaders
            }
        }
        finally {
            $response.Dispose()
        }
    }
    finally {
        $request.Dispose()
    }
}

function Convert-ResponseJson {
    param([Parameter(Mandatory = $true)]$Response)
    if ([string]::IsNullOrWhiteSpace($Response.Body)) {
        return $null
    }
    return $Response.Body | ConvertFrom-Json
}

function Get-SessionCookieValue {
    param(
        [Parameter(Mandatory = $true)]$ClientRecord,
        [Parameter(Mandatory = $true)][string]$BaseUri
    )
    foreach ($cookie in $ClientRecord.Handler.CookieContainer.GetCookies([uri]$BaseUri)) {
        if ($cookie.Name -eq "dayorder_session") {
            return $cookie.Value
        }
    }
    return $null
}

function Wait-Healthy {
    param(
        [Parameter(Mandatory = $true)][string]$Uri,
        [int]$TimeoutSeconds = 30
    )
    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
    $probe = New-DayOrderClient
    while ([DateTime]::UtcNow -lt $deadline) {
        try {
            $response = Invoke-DayOrder $probe "GET" $Uri
            if ($response.Status -eq 200) {
                return
            }
        }
        catch {
            # The process may still be compiling or binding its socket.
        }
        Start-Sleep -Milliseconds 150
    }
    throw "Timed out waiting for $Uri"
}

function Stop-ChildProcess {
    param($Process)
    if ($null -eq $Process) {
        return
    }
    try {
        if (-not $Process.HasExited) {
            Stop-Process -Id $Process.Id -Force
            [void]$Process.WaitForExit(5000)
        }
    }
    catch {
        Write-Warning "Could not stop child process $($Process.Id): $($_.Exception.Message)"
    }
}

function Start-DayOrderApi {
    param(
        [Parameter(Mandatory = $true)][string]$Binary,
        [Parameter(Mandatory = $true)][string]$Address,
        [Parameter(Mandatory = $true)][string]$Database,
        [Parameter(Mandatory = $true)][string]$AllowedOrigin,
        [string]$WebDirectory = ""
    )
    $env:DAYORDER_ADDR = $Address
    $env:DAYORDER_DB_PATH = $Database
    $env:DAYORDER_ALLOWED_ORIGINS = $AllowedOrigin
    $env:DAYORDER_WEB_DIR = $WebDirectory
    $script:apiRun++
    return Start-Process -FilePath $Binary -WorkingDirectory $repoRoot -PassThru -WindowStyle Hidden `
        -RedirectStandardOutput (Join-Path $tempRoot "api-$($script:apiRun).stdout.log") `
        -RedirectStandardError (Join-Path $tempRoot "api-$($script:apiRun).stderr.log")
}

function New-AppData {
    param(
        [Parameter(Mandatory = $true)][string]$Marker,
        [Parameter(Mandatory = $true)][string]$Title
    )
    return [ordered]@{
        version = 1
        goals = @([ordered]@{ id = $Marker; title = $Title })
        tasks = @()
        events = @()
        records = @()
        notes = @()
        reviews = @()
        agentRuns = @()
        audit = @()
        settings = [ordered]@{
            energy = 3
            aiEnabled = $true
            remindersEnabled = $false
            onboardingCompleted = $true
            focusAreas = @()
            dataMode = "local"
            localOnly = $true
            permissions = [ordered]@{
                goals = $true
                calendar = $true
                records = $true
                privateNotes = $false
            }
        }
    }
}

$savedEnvironment = @{}
foreach ($name in @("DAYORDER_ADDR", "DAYORDER_DB_PATH", "DAYORDER_ALLOWED_ORIGINS", "DAYORDER_WEB_DIR", "VITE_API_PROXY_TARGET")) {
    $savedEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, "Process")
}
$script:apiRun = 0

try {
    New-Item -ItemType Directory -Path $tempRoot | Out-Null
    $binary = Join-Path $tempRoot "dayorder-api.exe"
    $database = Join-Path $tempRoot "dayorder.db"
    $webDirectory = Join-Path $repoRoot "apps\web\dist"

    Write-Host "Building runtime acceptance binary..."
    & go build -o $binary ./apps/api/cmd/server
    if ($LASTEXITCODE -ne 0) {
        throw "go build failed with exit code $LASTEXITCODE"
    }

    $apiPort = Get-FreePort
    do { $webPort = Get-FreePort } while ($webPort -eq $apiPort)
    $apiBase = "http://127.0.0.1:$apiPort"
    $webBase = "http://127.0.0.1:$webPort"

    Write-Host "Starting Go API and Vite proxy..."
    $apiProcess = Start-DayOrderApi $binary "127.0.0.1:$apiPort" $database $webBase
    Wait-Healthy "$apiBase/api/v1/health"

    $env:VITE_API_PROXY_TARGET = $apiBase
    $nodeExecutable = (Get-Command node -ErrorAction Stop).Source
    $viteCli = Join-Path $repoRoot "node_modules\vite\bin\vite.js"
    $viteArguments = "`"$viteCli`" --host 127.0.0.1 --port $webPort --strictPort"
    $viteProcess = Start-Process -FilePath $nodeExecutable -WorkingDirectory (Join-Path $repoRoot "apps\web") -PassThru -WindowStyle Hidden `
        -ArgumentList $viteArguments `
        -RedirectStandardOutput (Join-Path $tempRoot "vite.stdout.log") `
        -RedirectStandardError (Join-Path $tempRoot "vite.stderr.log")
    Wait-Healthy "$webBase/api/v1/health"

    $anonymous = New-DayOrderClient
    $response = Invoke-DayOrder $anonymous "GET" "$webBase/api/v1/state"
    Assert-Status $response 401 "anonymous state access is rejected through Vite"
    Assert-True ((Convert-ResponseJson $response).code -eq "AUTH_REQUIRED") "anonymous rejection returns AUTH_REQUIRED"

    $userA = New-DayOrderClient
    $stateA1 = New-AppData "guest-migrated" "Guest data migrated at registration"
    $registerA = Invoke-DayOrder $userA "POST" "$webBase/api/v1/auth/register" ([ordered]@{
        displayName = "Runtime A"
        email = "runtime-a@example.com"
        password = "correct-horse-123"
        initialData = $stateA1
    }) @{ Origin = $webBase }
    Assert-Status $registerA 201 "registration works through the Vite proxy"
    $registerABody = Convert-ResponseJson $registerA
    Assert-True ($registerABody.state.revision -eq 1) "registration creates state revision 1 atomically"
    Assert-True ($registerABody.state.data.goals[0].id -eq "guest-migrated") "registration migrates the supplied guest AppData"
    $cookieHeader = [string]$registerA.Headers["Set-Cookie"]
    Assert-True ($cookieHeader -match "dayorder_session=") "registration sets the session cookie"
    Assert-True ($cookieHeader -match "(?i)HttpOnly") "session cookie is HttpOnly"
    Assert-True ($cookieHeader -match "(?i)SameSite=Lax") "session cookie uses SameSite=Lax"
    Assert-True ($cookieHeader -match "(?i)Path=/") "session cookie is scoped to the application"
    Assert-True ($cookieHeader -match "(?i)Max-Age=2592000") "session cookie lasts 30 days"
    Assert-True ($cookieHeader -notmatch "(?i);\s*Secure") "plain HTTP does not incorrectly emit Secure"
    Assert-True ($registerA.Headers["Cache-Control"] -eq "no-store") "authenticated responses are not cached"
    Assert-True ($registerA.Headers["X-Content-Type-Options"] -eq "nosniff") "security headers reach the proxy client"

    $loadedA = Invoke-DayOrder $userA "GET" "$webBase/api/v1/state"
    Assert-Status $loadedA 200 "migrated state can be read back"
    Assert-True ((Convert-ResponseJson $loadedA).data.goals[0].id -eq "guest-migrated") "migrated marker survives a round trip"

    $userB = New-DayOrderClient
    $registerB = Invoke-DayOrder $userB "POST" "$apiBase/api/v1/auth/register" ([ordered]@{
        displayName = "Runtime B"
        email = "runtime-b@example.com"
        password = "correct-horse-123"
        initialData = (New-AppData "user-b-only" "B private data")
    })
    Assert-Status $registerB 201 "a second account can be registered"

    $stateA2 = New-AppData "user-a-updated" "A private update"
    $saveA = Invoke-DayOrder $userA "PUT" "$apiBase/api/v1/state" ([ordered]@{
        expectedRevision = 1
        data = $stateA2
    })
    Assert-Status $saveA 200 "the first account can update its own state"
    Assert-True ((Convert-ResponseJson $saveA).revision -eq 2) "the first account revision advances independently"

    $loadedB = Invoke-DayOrder $userB "GET" "$apiBase/api/v1/state"
    Assert-Status $loadedB 200 "the second account can read its own state"
    $loadedBBody = Convert-ResponseJson $loadedB
    Assert-True ($loadedBBody.revision -eq 1) "the second account revision is isolated"
    Assert-True ($loadedBBody.data.goals[0].id -eq "user-b-only") "the second account cannot see the first account state"

    $userASecondSession = New-DayOrderClient
    $loginA = Invoke-DayOrder $userASecondSession "POST" "$webBase/api/v1/auth/login" ([ordered]@{
        email = "runtime-a@example.com"
        password = "correct-horse-123"
    }) @{ Origin = $webBase }
    Assert-Status $loginA 200 "existing-account login works through Vite"

    $profileA = Invoke-DayOrder $userASecondSession "PATCH" "$apiBase/api/v1/users/me" ([ordered]@{
        displayName = "Runtime A Updated"
    })
    Assert-Status $profileA 200 "profile updates are persisted"
    Assert-True ((Convert-ResponseJson $profileA).user.displayName -eq "Runtime A Updated") "updated display name is returned"

    $emailA = Invoke-DayOrder $userASecondSession "PUT" "$apiBase/api/v1/users/me/email" ([ordered]@{
        currentPassword = "correct-horse-123"
        email = "runtime-a-updated@example.com"
    })
    Assert-Status $emailA 200 "email changes require and accept the current password"
    Assert-True ((Convert-ResponseJson $emailA).user.email -eq "runtime-a-updated@example.com") "updated email is returned"

    $cookieBeforeRotation = Get-SessionCookieValue $userASecondSession $apiBase
    $passwordA = Invoke-DayOrder $userASecondSession "PUT" "$apiBase/api/v1/users/me/password" ([ordered]@{
        currentPassword = "correct-horse-123"
        password = "new-password-456"
    })
    Assert-Status $passwordA 204 "password change succeeds"
    $cookieAfterRotation = Get-SessionCookieValue $userASecondSession $apiBase
    Assert-True (-not [string]::IsNullOrWhiteSpace($cookieAfterRotation)) "password change leaves a valid current cookie"
    Assert-True ($cookieAfterRotation -ne $cookieBeforeRotation) "password change rotates the current session token"

    $revokedSession = Invoke-DayOrder $userA "GET" "$apiBase/api/v1/auth/session"
    Assert-Status $revokedSession 401 "password change revokes the other session"
    $currentSession = Invoke-DayOrder $userASecondSession "GET" "$apiBase/api/v1/auth/session"
    Assert-Status $currentSession 200 "the rotated current session remains valid"
    $currentSessionBody = Convert-ResponseJson $currentSession
    Assert-True ($currentSessionBody.user.email -eq "runtime-a-updated@example.com") "the current session reflects the email change"
    Assert-True ($currentSessionBody.user.displayName -eq "Runtime A Updated") "the current session reflects the profile change"

    $oldCredentials = New-DayOrderClient
    $oldLogin = Invoke-DayOrder $oldCredentials "POST" "$apiBase/api/v1/auth/login" ([ordered]@{
        email = "runtime-a@example.com"
        password = "correct-horse-123"
    })
    Assert-Status $oldLogin 401 "old credentials no longer authenticate"
    Assert-True ((Convert-ResponseJson $oldLogin).code -eq "INVALID_CREDENTIALS") "failed login does not disclose account details"

    $userAThirdSession = New-DayOrderClient
    $newLogin = Invoke-DayOrder $userAThirdSession "POST" "$apiBase/api/v1/auth/login" ([ordered]@{
        email = "runtime-a-updated@example.com"
        password = "new-password-456"
    })
    Assert-Status $newLogin 200 "new email and password authenticate"

    Write-Host "Restarting the Go service against the same SQLite database..."
    Stop-ChildProcess $apiProcess
    $apiProcess = $null
    $apiProcess = Start-DayOrderApi $binary "127.0.0.1:$apiPort" $database $webBase
    Wait-Healthy "$webBase/api/v1/health"

    $sessionAfterRestart = Invoke-DayOrder $userASecondSession "GET" "$webBase/api/v1/auth/session"
    Assert-Status $sessionAfterRestart 200 "session survives an API restart"
    $stateAfterRestart = Invoke-DayOrder $userASecondSession "GET" "$webBase/api/v1/state"
    Assert-Status $stateAfterRestart 200 "state survives an API restart"
    $stateAfterRestartBody = Convert-ResponseJson $stateAfterRestart
    Assert-True ($stateAfterRestartBody.revision -eq 2) "persisted state keeps its revision"
    Assert-True ($stateAfterRestartBody.data.goals[0].id -eq "user-a-updated") "persisted state keeps the account payload"

    Write-Host "Switching the Go service to production SPA hosting..."
    Stop-ChildProcess $viteProcess
    $viteProcess = $null
    Stop-ChildProcess $apiProcess
    $apiProcess = $null
    $apiProcess = Start-DayOrderApi $binary "127.0.0.1:$apiPort" $database $apiBase $webDirectory
    Wait-Healthy "$apiBase/api/v1/health"

    $deepLink = Invoke-DayOrder $anonymous "GET" "$apiBase/goals"
    Assert-Status $deepLink 200 "Go serves an SPA deep link"
    Assert-True ($deepLink.Body -match '<div id="root"></div>') "SPA deep link falls back to the built index"
    Assert-True ($deepLink.Headers["Cache-Control"] -eq "no-cache") "SPA shell is not cached permanently"
    $staticHealth = Invoke-DayOrder $anonymous "GET" "$apiBase/api/v1/health"
    Assert-Status $staticHealth 200 "API routes remain available beside the SPA"
    Assert-True ((Convert-ResponseJson $staticHealth).status -eq "ok") "static hosting does not shadow API routes"

    $logout = Invoke-DayOrder $userASecondSession "POST" "$apiBase/api/v1/auth/logout" ([ordered]@{})
    Assert-Status $logout 204 "logout revokes the current session"
    Assert-True ([string]$logout.Headers["Set-Cookie"] -match "(?i)Max-Age=0") "logout expires the browser cookie"
    $afterLogout = Invoke-DayOrder $userASecondSession "GET" "$apiBase/api/v1/auth/session"
    Assert-Status $afterLogout 401 "logged-out session cannot be reused"
    $otherCurrentSession = Invoke-DayOrder $userAThirdSession "GET" "$apiBase/api/v1/auth/session"
    Assert-Status $otherCurrentSession 200 "logout does not revoke another current login"

    Write-Host "Runtime acceptance passed: $checks checks."
}
catch {
    Write-Error $_
    if (Test-Path -LiteralPath $tempRoot) {
        Write-Host "Runtime logs: $tempRoot"
    }
    exit 1
}
finally {
    Stop-ChildProcess $viteProcess
    Stop-ChildProcess $apiProcess
    foreach ($record in $clients) {
        $record.Client.Dispose()
        $record.Handler.Dispose()
    }
    foreach ($name in $savedEnvironment.Keys) {
        [Environment]::SetEnvironmentVariable($name, $savedEnvironment[$name], "Process")
    }
    if (Test-Path -LiteralPath $tempRoot) {
        $resolvedTempRoot = [System.IO.Path]::GetFullPath($tempRoot)
        if ($resolvedTempRoot.StartsWith($tempBase, [System.StringComparison]::OrdinalIgnoreCase) -and
            ([System.IO.Path]::GetFileName($resolvedTempRoot) -like "dayorder-runtime-*")) {
            Remove-Item -LiteralPath $resolvedTempRoot -Recurse -Force
        }
    }
}
