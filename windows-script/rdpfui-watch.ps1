param(
    [string]$ConfigPath = "$env:LOCALAPPDATA\rdpfui-script\config.json"
)

Set-StrictMode -Version 3.0
$ErrorActionPreference = "Stop"

function Expand-RdpfuiPath {
    param([Parameter(Mandatory = $true)][string]$Path)

    return [Environment]::ExpandEnvironmentVariables($Path)
}

function Get-Config {
    param([Parameter(Mandatory = $true)][string]$Path)

    if (-not (Test-Path -LiteralPath $Path)) {
        throw "Config file not found: $Path"
    }

    $raw = Get-Content -LiteralPath $Path -Raw
    return $raw | ConvertFrom-Json
}

function New-Directory {
    param([Parameter(Mandatory = $true)][string]$Path)

    if (-not (Test-Path -LiteralPath $Path)) {
        New-Item -ItemType Directory -Path $Path -Force | Out-Null
    }
}

function Get-JsonValue {
    param(
        [Parameter(Mandatory = $true)]$Object,
        [Parameter(Mandatory = $true)][string]$Name,
        $Default = $null
    )

    if ($Object.PSObject.Properties.Name -contains $Name) {
        return $Object.PSObject.Properties[$Name].Value
    }

    return $Default
}

$script:Config = Get-Config -Path $ConfigPath
$script:AppRoot = Split-Path -Parent $ConfigPath
$script:StagingFolder = Expand-RdpfuiPath ([string](Get-JsonValue -Object $script:Config -Name "stagingFolder" -Default "$env:LOCALAPPDATA\rdpfui-script\staging"))
$script:LogFolder = Expand-RdpfuiPath ([string](Get-JsonValue -Object $script:Config -Name "logFolder" -Default "$env:LOCALAPPDATA\rdpfui-script\logs"))
$script:PollIntervalSeconds = [int](Get-JsonValue -Object $script:Config -Name "pollIntervalSeconds" -Default 3)
$script:MaxFileSizeKB = [int64](Get-JsonValue -Object $script:Config -Name "maxFileSizeKB" -Default 256)
$script:AllowFoldersWithoutRegex = [bool](Get-JsonValue -Object $script:Config -Name "allowFoldersWithoutRegex" -Default $false)
$script:BlockedDirectives = @(Get-JsonValue -Object $script:Config -Name "blockedDirectives" -Default @())
$script:LogFile = Join-Path $script:LogFolder ("rdpfui-watch-{0}.log" -f (Get-Date -Format "yyyyMMdd"))
$script:Processed = @{}

New-Directory -Path $script:StagingFolder
New-Directory -Path $script:LogFolder

function Write-RdpfuiLog {
    param([Parameter(Mandatory = $true)][string]$Message)

    $line = "{0} {1}" -f (Get-Date -Format "yyyy-MM-dd HH:mm:ss"), $Message
    Add-Content -LiteralPath $script:LogFile -Value $line -Encoding UTF8
}

function Get-ConfiguredFolders {
    foreach ($folder in @(Get-JsonValue -Object $script:Config -Name "folders" -Default @())) {
        $rawPath = Get-JsonValue -Object $folder -Name "path" -Default ""
        if ($null -eq $rawPath -or [string]::IsNullOrWhiteSpace([string]$rawPath)) {
            continue
        }

        $path = Expand-RdpfuiPath ([string]$rawPath)
        if (-not (Test-Path -LiteralPath $path -PathType Container)) {
            Write-RdpfuiLog "Skipping missing folder: $path"
            continue
        }

        [PSCustomObject]@{
            Path = (Resolve-Path -LiteralPath $path).Path
            Recursive = [bool](Get-JsonValue -Object $folder -Name "recursive" -Default $false)
            AllowWithoutRegex = [bool](Get-JsonValue -Object $folder -Name "allowWithoutRegex" -Default $false)
            ContentRegex = @(Get-JsonValue -Object $folder -Name "contentRegex" -Default @())
        }
    }
}

function Test-PathUnderFolder {
    param(
        [Parameter(Mandatory = $true)][string]$FolderPath,
        [Parameter(Mandatory = $true)][bool]$Recursive,
        [Parameter(Mandatory = $true)][string]$FilePath
    )

    $folderFull = [System.IO.Path]::GetFullPath($FolderPath).TrimEnd([System.IO.Path]::DirectorySeparatorChar)
    $fileFull = [System.IO.Path]::GetFullPath($FilePath)

    if ($Recursive) {
        return $fileFull.StartsWith($folderFull + [System.IO.Path]::DirectorySeparatorChar, [System.StringComparison]::OrdinalIgnoreCase)
    }

    return [string]::Equals([System.IO.Path]::GetDirectoryName($fileFull), $folderFull, [System.StringComparison]::OrdinalIgnoreCase)
}

function Get-MatchingFolder {
    param([Parameter(Mandatory = $true)][string]$FilePath)

    foreach ($folder in Get-ConfiguredFolders) {
        if (Test-PathUnderFolder -FolderPath $folder.Path -Recursive $folder.Recursive -FilePath $FilePath) {
            return $folder
        }
    }

    return $null
}

function Test-RdpFileSafe {
    param(
        [Parameter(Mandatory = $true)][string]$FilePath,
        [Parameter(Mandatory = $true)]$Folder
    )

    if (-not [string]::Equals([System.IO.Path]::GetExtension($FilePath), ".rdp", [System.StringComparison]::OrdinalIgnoreCase)) {
        return $false
    }

    $file = Get-Item -LiteralPath $FilePath -ErrorAction Stop
    $maxBytes = $script:MaxFileSizeKB * 1024
    if ($maxBytes -gt 0 -and $file.Length -gt $maxBytes) {
        Write-RdpfuiLog "Rejected oversized file: $FilePath"
        return $false
    }

    $text = Get-Content -LiteralPath $FilePath -Raw -ErrorAction Stop
    $text = $text -replace "`r`n", "`n" -replace "`r", "`n"

    foreach ($directive in $script:BlockedDirectives) {
        if ([string]::IsNullOrWhiteSpace([string]$directive)) {
            continue
        }

        $pattern = "(?im)^\s*" + [regex]::Escape([string]$directive)
        if ($text -match $pattern) {
            Write-RdpfuiLog "Rejected blocked directive '$directive': $FilePath"
            return $false
        }
    }

    $regexes = @($Folder.ContentRegex) | Where-Object { -not [string]::IsNullOrWhiteSpace([string]$_) }
    if ($regexes.Count -eq 0) {
        if ($script:AllowFoldersWithoutRegex -or [bool]$Folder.AllowWithoutRegex) {
            return $true
        }

        Write-RdpfuiLog "Rejected because no contentRegex is configured for folder: $FilePath"
        return $false
    }

    foreach ($regex in $regexes) {
        try {
            if ($text -match ([string]$regex)) {
                return $true
            }
        } catch {
            Write-RdpfuiLog "Invalid contentRegex '$regex': $($_.Exception.Message)"
        }
    }

    Write-RdpfuiLog "Rejected missing regex match: $FilePath"
    return $false
}

function Wait-FileReady {
    param([Parameter(Mandatory = $true)][string]$FilePath)

    for ($i = 0; $i -lt 20; $i++) {
        try {
            $stream = [System.IO.File]::Open($FilePath, [System.IO.FileMode]::Open, [System.IO.FileAccess]::ReadWrite, [System.IO.FileShare]::None)
            $stream.Close()
            return $true
        } catch {
            Start-Sleep -Milliseconds 250
        }
    }

    return $false
}

function Invoke-RdpFile {
    param([Parameter(Mandatory = $true)][string]$FilePath)

    if (-not (Test-Path -LiteralPath $FilePath -PathType Leaf)) {
        return
    }

    $folder = Get-MatchingFolder -FilePath $FilePath
    if ($null -eq $folder) {
        return
    }

    $key = [System.IO.Path]::GetFullPath($FilePath).ToLowerInvariant()
    if ($script:Processed.ContainsKey($key)) {
        return
    }
    $script:Processed[$key] = $true

    if (-not (Wait-FileReady -FilePath $FilePath)) {
        Write-RdpfuiLog "Skipped locked file: $FilePath"
        return
    }

    if (-not (Test-RdpFileSafe -FilePath $FilePath -Folder $folder)) {
        return
    }

    $timestamp = Get-Date -Format "yyyyMMdd-HHmmss-fff"
    $safeName = [System.IO.Path]::GetFileName($FilePath)
    $destination = Join-Path $script:StagingFolder "$timestamp-$safeName"

    Move-Item -LiteralPath $FilePath -Destination $destination -Force
    Write-RdpfuiLog "Opening via mstsc.exe: $destination"
    Start-Process -FilePath "$env:WINDIR\System32\mstsc.exe" -ArgumentList "`"$destination`""
}

function Scan-ExistingFiles {
    foreach ($folder in Get-ConfiguredFolders) {
        $searchOption = if ($folder.Recursive) { "AllDirectories" } else { "TopDirectoryOnly" }
        try {
            [System.IO.Directory]::EnumerateFiles($folder.Path, "*.rdp", $searchOption) | ForEach-Object {
                Invoke-RdpFile -FilePath $_
            }
        } catch {
            Write-RdpfuiLog "Scan failed for $($folder.Path): $($_.Exception.Message)"
        }
    }
}

function Start-Watchers {
    $watchers = New-Object System.Collections.Generic.List[System.IO.FileSystemWatcher]

    foreach ($folder in Get-ConfiguredFolders) {
        $watcher = New-Object System.IO.FileSystemWatcher
        $watcher.Path = $folder.Path
        $watcher.Filter = "*.rdp"
        $watcher.IncludeSubdirectories = $folder.Recursive
        $watcher.EnableRaisingEvents = $true

        Register-ObjectEvent -InputObject $watcher -EventName Created -SourceIdentifier "rdpfui.created.$($watchers.Count)" | Out-Null
        Register-ObjectEvent -InputObject $watcher -EventName Changed -SourceIdentifier "rdpfui.changed.$($watchers.Count)" | Out-Null
        Register-ObjectEvent -InputObject $watcher -EventName Renamed -SourceIdentifier "rdpfui.renamed.$($watchers.Count)" | Out-Null

        $watchers.Add($watcher)
        Write-RdpfuiLog "Watching $($folder.Path), recursive=$($folder.Recursive)"
    }

    return $watchers
}

Write-RdpfuiLog "rdpfui script watcher starting"
Scan-ExistingFiles
$watchers = Start-Watchers

try {
    while ($true) {
        $event = Wait-Event -Timeout $script:PollIntervalSeconds
        if ($null -eq $event) {
            continue
        }

        try {
            $path = $event.SourceEventArgs.FullPath
            if (-not [string]::IsNullOrWhiteSpace($path)) {
                Invoke-RdpFile -FilePath $path
            }
        } catch {
            Write-RdpfuiLog "Event failed: $($_.Exception.Message)"
        } finally {
            Remove-Event -EventIdentifier $event.EventIdentifier -ErrorAction SilentlyContinue
        }
    }
} finally {
    foreach ($watcher in $watchers) {
        $watcher.EnableRaisingEvents = $false
        $watcher.Dispose()
    }
}
