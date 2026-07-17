param(
    [ValidateSet("Auto", "Install", "Repair", "Upgrade", "Uninstall")]
    [string]$Mode = "Auto"
)

Set-StrictMode -Version 3.0
$ErrorActionPreference = "Stop"

$TaskName = "rdpfui-script"
$InstallRoot = Join-Path $env:LOCALAPPDATA "rdpfui-script"
$ConfigPath = Join-Path $InstallRoot "config.json"
$VersionPath = Join-Path $InstallRoot "VERSION"
$SourceRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$SourceVersionPath = Join-Path $SourceRoot "VERSION"
$SourceVersion = (Get-Content -LiteralPath $SourceVersionPath -Raw).Trim()

function Write-Info {
    param([Parameter(Mandatory = $true)][string]$Message)
    Write-Host "[rdpfui] $Message"
}

function Get-InstalledVersion {
    if (Test-Path -LiteralPath $VersionPath -PathType Leaf) {
        return (Get-Content -LiteralPath $VersionPath -Raw).Trim()
    }

    return $null
}

function Test-TaskInstalled {
    try {
        $null = Get-ScheduledTask -TaskName $TaskName -ErrorAction Stop
        return $true
    } catch {
        $null = schtasks.exe /Query /TN $TaskName 2>$null
        return $LASTEXITCODE -eq 0
    }
}

function Read-Choice {
    param(
        [Parameter(Mandatory = $true)][string]$Prompt,
        [Parameter(Mandatory = $true)][string[]]$Allowed,
        [Parameter(Mandatory = $true)][string]$Default
    )

    while ($true) {
        $answer = Read-Host "$Prompt [$Default]"
        if ([string]::IsNullOrWhiteSpace($answer)) {
            $answer = $Default
        }

        foreach ($choice in $Allowed) {
            if ([string]::Equals($answer, $choice, [System.StringComparison]::OrdinalIgnoreCase)) {
                return $choice
            }
        }

        Write-Host "Allowed choices: $($Allowed -join ', ')"
    }
}

function Copy-AppFiles {
    if (-not (Test-Path -LiteralPath $InstallRoot)) {
        New-Item -ItemType Directory -Path $InstallRoot -Force | Out-Null
    }

    Copy-Item -LiteralPath (Join-Path $SourceRoot "rdpfui-watch.ps1") -Destination (Join-Path $InstallRoot "rdpfui-watch.ps1") -Force
    Copy-Item -LiteralPath $SourceVersionPath -Destination $VersionPath -Force

    if (-not (Test-Path -LiteralPath $ConfigPath -PathType Leaf)) {
        Copy-Item -LiteralPath (Join-Path $SourceRoot "config.example.json") -Destination $ConfigPath -Force
        Write-Info "Created default config: $ConfigPath"
    } else {
        Write-Info "Keeping existing config: $ConfigPath"
    }

    foreach ($dir in @("logs", "staging")) {
        $path = Join-Path $InstallRoot $dir
        if (-not (Test-Path -LiteralPath $path)) {
            New-Item -ItemType Directory -Path $path -Force | Out-Null
        }
    }
}

function Install-Task {
    $watchScript = Join-Path $InstallRoot "rdpfui-watch.ps1"
    $powershell = Join-Path $env:WINDIR "System32\WindowsPowerShell\v1.0\powershell.exe"

    if (-not (Test-Path -LiteralPath $powershell -PathType Leaf)) {
        throw "Windows PowerShell not found: $powershell"
    }
    if (-not (Test-Path -LiteralPath $watchScript -PathType Leaf)) {
        throw "Watcher script not found: $watchScript"
    }
    if (-not (Test-Path -LiteralPath $ConfigPath -PathType Leaf)) {
        throw "Config file not found: $ConfigPath"
    }

    $arguments = "-NoProfile -ExecutionPolicy Bypass -WindowStyle Hidden -File `"$watchScript`" -ConfigPath `"$ConfigPath`""
    $currentUser = [System.Security.Principal.WindowsIdentity]::GetCurrent().Name

    try {
        $action = New-ScheduledTaskAction -Execute $powershell -Argument $arguments -WorkingDirectory $InstallRoot
        $trigger = New-ScheduledTaskTrigger -AtLogOn -User $currentUser
        $principal = New-ScheduledTaskPrincipal -UserId $currentUser -LogonType Interactive -RunLevel LeastPrivilege
        $settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -ExecutionTimeLimit ([TimeSpan]::Zero) -MultipleInstances IgnoreNew

        Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger -Principal $principal -Settings $settings -Force | Out-Null
    } catch {
        throw "Failed to create scheduled task '$TaskName': $($_.Exception.Message)"
    }

    Write-Info "Scheduled task installed: $TaskName"
}

function Install-App {
    Copy-AppFiles
    Install-Task
    Write-Info "Installed version $SourceVersion to $InstallRoot"
}

function Uninstall-App {
    if (Test-TaskInstalled) {
        try {
            Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false -ErrorAction Stop
        } catch {
            $null = schtasks.exe /Delete /TN $TaskName /F
            if ($LASTEXITCODE -ne 0) {
                throw "Failed to delete scheduled task '$TaskName'."
            }
        }
        Write-Info "Removed scheduled task: $TaskName"
    } else {
        Write-Info "Scheduled task is not installed."
    }

    $removeFiles = Read-Choice -Prompt "Remove installed files and config from $InstallRoot? y/n" -Allowed @("y", "n") -Default "n"
    if ($removeFiles -eq "y" -and (Test-Path -LiteralPath $InstallRoot)) {
        Remove-Item -LiteralPath $InstallRoot -Recurse -Force
        Write-Info "Removed $InstallRoot"
    }
}

$installedVersion = Get-InstalledVersion
$taskInstalled = Test-TaskInstalled

if ($Mode -eq "Auto") {
    if ($null -eq $installedVersion -and -not $taskInstalled) {
        $choice = Read-Choice -Prompt "rdpfui script is not installed. Install? y/n" -Allowed @("y", "n") -Default "y"
        if ($choice -eq "n") {
            exit 0
        }
        $Mode = "Install"
    } elseif ($installedVersion -eq $SourceVersion) {
        $Mode = Read-Choice -Prompt "Version $installedVersion is installed. Choose repair, uninstall, or cancel" -Allowed @("repair", "uninstall", "cancel") -Default "repair"
    } else {
        Write-Info "Installed version: $installedVersion"
        Write-Info "Package version:   $SourceVersion"
        $Mode = Read-Choice -Prompt "Choose upgrade, repair, uninstall, or cancel" -Allowed @("upgrade", "repair", "uninstall", "cancel") -Default "upgrade"
    }
}

switch ($Mode.ToLowerInvariant()) {
    "install" { Install-App }
    "repair" { Install-App }
    "upgrade" { Install-App }
    "uninstall" { Uninstall-App }
    "cancel" { Write-Info "Cancelled." }
    default { throw "Unsupported mode: $Mode" }
}
