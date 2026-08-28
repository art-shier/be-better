[CmdletBinding()]
param(
    [switch]$RequireDocker,
    [string]$ProductionBaseUrl,
    [string]$ProductionEnvironmentFile,
    [string]$ProductionProjectName,
    [switch]$InsecureProductionTLS
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

$postgresArguments = @{}
if ($RequireDocker) { $postgresArguments.RequireDocker = $true }
& (Join-Path $PSScriptRoot "runtime-postgres-acceptance.ps1") @postgresArguments

$securityArguments = @{}
if ($ProductionBaseUrl) { $securityArguments.BaseUrl = $ProductionBaseUrl }
if ($ProductionEnvironmentFile) { $securityArguments.EnvironmentFile = $ProductionEnvironmentFile }
if ($ProductionProjectName) { $securityArguments.ProjectName = $ProductionProjectName }
if ($InsecureProductionTLS) { $securityArguments.Insecure = $true }
& (Join-Path $PSScriptRoot "security-acceptance.ps1") @securityArguments

Write-Host "DayOrder runtime acceptance completed."
