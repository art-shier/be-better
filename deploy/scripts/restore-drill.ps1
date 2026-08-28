[CmdletBinding()]
param(
    [switch]$Force,
    [switch]$KeepRestoredVolume,
    [string]$DeletionManifest,
    [string]$ComposeFile = (Join-Path $PSScriptRoot "..\compose.yaml"),
    [string]$EnvironmentFile = (Join-Path $PSScriptRoot "..\.env.production"),
    [string]$MetricsFile = (Join-Path $PSScriptRoot "..\metrics\dayorder_restore.prom")
)

$ErrorActionPreference = "Stop"
$restoreVolume = "dayorder-postgres-restore"

if (-not $Force) {
    throw "Restore drill resets only the isolated Docker volume '$restoreVolume'. Re-run with -Force to confirm."
}

function Invoke-DayOrderDocker {
    param([Parameter(Mandatory)][string[]]$Arguments)
    $output = & docker @Arguments 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw "docker command failed: $($output -join [Environment]::NewLine)"
    }
    return $output
}

function Write-RestoreMetrics {
    param([double]$Success, [double]$Duration)
    $directory = Split-Path -Parent $MetricsFile
    New-Item -ItemType Directory -Path $directory -Force | Out-Null
    $temporary = "$MetricsFile.tmp"
    $timestamp = [DateTimeOffset]::UtcNow.ToUnixTimeSeconds()
    $lines = @(
        "# HELP dayorder_restore_drill_success Whether the latest isolated restore drill succeeded."
        "# TYPE dayorder_restore_drill_success gauge"
        "dayorder_restore_drill_success $Success"
        "# HELP dayorder_restore_drill_last_success_timestamp_seconds Unix timestamp of the latest successful drill."
        "# TYPE dayorder_restore_drill_last_success_timestamp_seconds gauge"
        "dayorder_restore_drill_last_success_timestamp_seconds $($(if ($Success -eq 1) { $timestamp } else { 0 }))"
        "# HELP dayorder_restore_drill_duration_seconds Duration of the latest restore drill."
        "# TYPE dayorder_restore_drill_duration_seconds gauge"
        "dayorder_restore_drill_duration_seconds $Duration"
        ""
    )
    [IO.File]::WriteAllLines($temporary, $lines, [Text.UTF8Encoding]::new($false))
    Move-Item -LiteralPath $temporary -Destination $MetricsFile -Force
}

$composePath = (Resolve-Path -LiteralPath $ComposeFile).Path
$environmentPath = (Resolve-Path -LiteralPath $EnvironmentFile).Path
$manifestPath = $null
if ($DeletionManifest) {
    $manifestPath = (Resolve-Path -LiteralPath $DeletionManifest).Path
    if ([IO.Path]::GetExtension($manifestPath) -ne ".sql") {
        throw "Deletion manifest must be a reviewed .sql file"
    }
}
$common = @("compose", "--env-file", $environmentPath, "-f", $composePath, "--profile", "restore")
$started = Get-Date

try {
    Invoke-DayOrderDocker -Arguments ($common + @("rm", "-s", "-f", "postgres-restore")) | Out-Null
    $existing = & docker volume inspect $restoreVolume 2>$null
    if ($LASTEXITCODE -eq 0) {
        Invoke-DayOrderDocker -Arguments @("volume", "rm", $restoreVolume) | Out-Null
    }

    Invoke-DayOrderDocker -Arguments ($common + @(
        "run", "--rm", "--no-deps", "--entrypoint", "dayorder-pgbackrest", "postgres-restore",
        "--stanza=dayorder", "--repo=1", "restore"
    )) | Out-Null
    Invoke-DayOrderDocker -Arguments ($common + @("up", "-d", "postgres-restore")) | Out-Null

    $deadline = (Get-Date).AddMinutes(5)
    do {
        $containerID = (Invoke-DayOrderDocker -Arguments ($common + @("ps", "-q", "postgres-restore")) | Select-Object -First 1).Trim()
        if ($containerID) {
            $health = (& docker inspect --format "{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}" $containerID 2>$null)
            if ($health -eq "healthy") { break }
            if ($health -eq "unhealthy" -or $health -eq "exited") { throw "restored PostgreSQL became $health" }
        }
        Start-Sleep -Seconds 2
    } while ((Get-Date) -lt $deadline)
    if ($health -ne "healthy") { throw "restored PostgreSQL did not become healthy within five minutes" }

    if ($manifestPath) {
        Invoke-DayOrderDocker -Arguments ($common + @("cp", $manifestPath, "postgres-restore:/tmp/deletion-manifest.sql")) | Out-Null
        Invoke-DayOrderDocker -Arguments ($common + @(
            "exec", "-T", "postgres-restore", "sh", "-ceu",
            'export PGPASSWORD="$(cat /run/secrets/postgres_admin_password)"; exec psql -v ON_ERROR_STOP=1 -U "$POSTGRES_USER" -d "$POSTGRES_DB" -f /tmp/deletion-manifest.sql'
        )) | Out-Null
    }

    $verificationSQL = @'
SELECT version, dirty FROM dayorder.schema_migrations;
SELECT count(*) = 0 AS rls_missing
FROM pg_catalog.pg_class
WHERE relnamespace = 'dayorder'::regnamespace
  AND relkind = 'r'
  AND relname IN ('goals', 'tasks', 'calendar_events', 'records', 'notes')
  AND NOT relrowsecurity;
SELECT * FROM dayorder.outbox_metrics();
'@
    Invoke-DayOrderDocker -Arguments ($common + @(
        "exec", "-T", "postgres-restore", "sh", "-ceu",
        "export PGPASSWORD=`"`$(cat /run/secrets/postgres_admin_password)`"; exec psql -v ON_ERROR_STOP=1 -U `"`$POSTGRES_USER`" -d `"`$POSTGRES_DB`" -c `"$verificationSQL`""
    )) | Out-Null

    $duration = ((Get-Date) - $started).TotalSeconds
    if ($duration -gt 3600) { throw "restore drill exceeded the 60-minute RTO" }
    Write-RestoreMetrics -Success 1 -Duration $duration
    Write-Host "Restore drill passed in $([Math]::Round($duration, 1)) seconds."
}
catch {
    Write-RestoreMetrics -Success 0 -Duration (((Get-Date) - $started).TotalSeconds)
    throw
}
finally {
    if (-not $KeepRestoredVolume) {
        try { Invoke-DayOrderDocker -Arguments ($common + @("rm", "-s", "-f", "postgres-restore")) | Out-Null } catch { Write-Warning $_ }
        $existing = & docker volume inspect $restoreVolume 2>$null
        if ($LASTEXITCODE -eq 0) {
            try { Invoke-DayOrderDocker -Arguments @("volume", "rm", $restoreVolume) | Out-Null } catch { Write-Warning $_ }
        }
    }
}
