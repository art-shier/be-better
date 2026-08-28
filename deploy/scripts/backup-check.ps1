[CmdletBinding()]
param(
    [ValidateSet("full", "diff", "incr")]
    [string]$BackupType = "full",
    [switch]$RunBackup,
    [string]$ComposeFile = (Join-Path $PSScriptRoot "..\compose.yaml"),
    [string]$EnvironmentFile = (Join-Path $PSScriptRoot "..\.env.production"),
    [string]$MetricsFile = (Join-Path $PSScriptRoot "..\metrics\dayorder_backup.prom")
)

$ErrorActionPreference = "Stop"

function Invoke-DayOrderDocker {
    param([Parameter(Mandatory)][string[]]$Arguments)
    $output = & docker @Arguments 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw "docker command failed: $($output -join [Environment]::NewLine)"
    }
    return $output
}

function Write-BackupMetrics {
    param(
        [double]$Success,
        [double]$LastSuccess,
        [double]$Age,
        [double]$Duration
    )
    $directory = Split-Path -Parent $MetricsFile
    New-Item -ItemType Directory -Path $directory -Force | Out-Null
    $temporary = "$MetricsFile.tmp"
    $lines = @(
        "# HELP dayorder_backup_check_success Whether the latest pgBackRest verification succeeded."
        "# TYPE dayorder_backup_check_success gauge"
        "dayorder_backup_check_success $Success"
        "# HELP dayorder_backup_last_success_timestamp_seconds Unix timestamp of the newest completed backup."
        "# TYPE dayorder_backup_last_success_timestamp_seconds gauge"
        "dayorder_backup_last_success_timestamp_seconds $LastSuccess"
        "# HELP dayorder_backup_age_seconds Age of the newest completed backup."
        "# TYPE dayorder_backup_age_seconds gauge"
        "dayorder_backup_age_seconds $Age"
        "# HELP dayorder_backup_duration_seconds Duration of the backup/check operation."
        "# TYPE dayorder_backup_duration_seconds gauge"
        "dayorder_backup_duration_seconds $Duration"
        ""
    )
    [IO.File]::WriteAllLines($temporary, $lines, [Text.UTF8Encoding]::new($false))
    Move-Item -LiteralPath $temporary -Destination $MetricsFile -Force
}

$composePath = (Resolve-Path -LiteralPath $ComposeFile).Path
$environmentPath = (Resolve-Path -LiteralPath $EnvironmentFile).Path
$common = @("compose", "--env-file", $environmentPath, "-f", $composePath)
$started = Get-Date

try {
    if ($RunBackup) {
        Invoke-DayOrderDocker -Arguments ($common + @("exec", "-T", "postgres", "dayorder-pgbackrest", "--stanza=dayorder", "backup", "--type=$BackupType")) | Out-Null
    }
    Invoke-DayOrderDocker -Arguments ($common + @("exec", "-T", "postgres", "dayorder-pgbackrest", "--stanza=dayorder", "check")) | Out-Null
    $rawInfo = Invoke-DayOrderDocker -Arguments ($common + @("exec", "-T", "postgres", "dayorder-pgbackrest", "--stanza=dayorder", "--log-level-console=off", "--output=json", "info"))
    $info = ($rawInfo -join [Environment]::NewLine) | ConvertFrom-Json
    $backups = @($info)[0].backup
    if (-not $backups -or @($backups).Count -eq 0) {
        throw "pgBackRest has no completed backup"
    }
    $lastSuccess = [double](@($backups | ForEach-Object { $_.timestamp.stop } | Measure-Object -Maximum).Maximum)
    $now = [DateTimeOffset]::UtcNow.ToUnixTimeSeconds()
    $age = [Math]::Max(0, $now - $lastSuccess)
    if ($age -gt 90000) {
        throw "latest completed backup is older than 25 hours ($age seconds)"
    }
    $duration = ((Get-Date) - $started).TotalSeconds
    Write-BackupMetrics -Success 1 -LastSuccess $lastSuccess -Age $age -Duration $duration
    Write-Host "pgBackRest check passed; newest backup age is $([Math]::Round($age / 3600, 2)) hours."
}
catch {
    $duration = ((Get-Date) - $started).TotalSeconds
    Write-BackupMetrics -Success 0 -LastSuccess 0 -Age 0 -Duration $duration
    throw
}
