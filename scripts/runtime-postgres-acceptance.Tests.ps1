$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$scriptPath = Join-Path $PSScriptRoot "runtime-postgres-acceptance.ps1"
$tokens = $null
$parseErrors = $null
$ast = [System.Management.Automation.Language.Parser]::ParseFile($scriptPath, [ref]$tokens, [ref]$parseErrors)
if ($parseErrors.Count -gt 0) {
    throw "Could not parse runtime PostgreSQL acceptance script: $($parseErrors[0].Message)"
}

$invokeJsonDefinition = $ast.Find({
    param($node)
    $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq "Invoke-Json"
}, $true)
if ($null -eq $invokeJsonDefinition) {
    throw "Invoke-Json was not found in runtime PostgreSQL acceptance script"
}

Invoke-Expression $invokeJsonDefinition.Extent.Text

function Invoke-WebRequest {
    [CmdletBinding()]
    param(
        [switch]$UseBasicParsing,
        [string]$Method,
        [string]$Uri,
        [Microsoft.PowerShell.Commands.WebRequestSession]$WebSession,
        [hashtable]$Headers,
        [int]$TimeoutSec,
        [string]$ContentType,
        [string]$Body
    )
    throw [System.InvalidOperationException]::new("simulated transport failure")
}

$caught = $null
try {
    Invoke-Json "GET" "http://127.0.0.1:1/transport-failure" ([Microsoft.PowerShell.Commands.WebRequestSession]::new()) | Out-Null
}
catch {
    $caught = $_
}

if ($null -eq $caught) {
    throw "Invoke-Json did not rethrow the simulated transport failure"
}
if ($caught.Exception.Message -ne "simulated transport failure") {
    throw "Invoke-Json replaced the transport failure with: $($caught.Exception.Message)"
}

Write-Host "PASS: Invoke-Json preserves transport exceptions without an HTTP response."

$newMutationHeadersDefinition = $ast.Find({
    param($node)
    $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq "New-MutationHeaders"
}, $true)
if ($null -eq $newMutationHeadersDefinition) {
    throw "New-MutationHeaders was not found in runtime PostgreSQL acceptance script"
}
Invoke-Expression $newMutationHeadersDefinition.Extent.Text

$mutationHeaders = New-MutationHeaders ([guid]::NewGuid().ToString()) 7
if ($mutationHeaders["If-Match"] -ne '"7"') {
    throw "New-MutationHeaders emitted an invalid If-Match entity tag: $($mutationHeaders["If-Match"])"
}

Write-Host "PASS: mutation versions use quoted If-Match entity tags."

$writeProcessLogsDefinition = $ast.Find({
    param($node)
    $node -is [System.Management.Automation.Language.FunctionDefinitionAst] -and $node.Name -eq "Write-ProcessLogs"
}, $true)
if ($null -eq $writeProcessLogsDefinition) {
    throw "Write-ProcessLogs was not found in runtime PostgreSQL acceptance script"
}
Invoke-Expression $writeProcessLogsDefinition.Extent.Text

$testLogRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("dayorder-runtime-log-test-" + [guid]::NewGuid().ToString("N"))
try {
    New-Item -ItemType Directory -Path $testLogRoot | Out-Null
    [System.IO.File]::WriteAllText((Join-Path $testLogRoot "api.stderr.log"), "simulated API panic")
    $logOutput = (Write-ProcessLogs $testLogRoot 6>&1 | Out-String)
    if ($logOutput -notmatch "api\.stderr\.log" -or $logOutput -notmatch "simulated API panic") {
        throw "Write-ProcessLogs did not emit the API log name and contents"
    }
}
finally {
    if (Test-Path -LiteralPath $testLogRoot) {
        Remove-Item -LiteralPath $testLogRoot -Recurse -Force
    }
}

Write-Host "PASS: runtime failure diagnostics emit captured process logs before cleanup."
