# rdpfui Windows script edition

This is the Windows-only, no-UI edition of `rdpfui`. It watches configured folders for `.rdp` files, validates them, moves accepted files to a staging folder, and opens them with `mstsc.exe`.

## Install

Run:

```bat
install.bat
```

The installer runs in the current user context. It copies files to:

```text
%LOCALAPPDATA%\rdpfui-script
```

Then it creates this scheduled task:

```text
rdpfui-script
```

The task starts the watcher at user logon. It does not require administrator rights.

After installation, the zip and extracted package folder can be deleted. The installed folder contains `install.bat`, `uninstall.bat`, and `rdpfui-install.ps1` for later repair, upgrade, or uninstall operations.

## Uninstall

Run:

```bat
uninstall.bat
```

The uninstaller removes the scheduled task and asks whether installed files and config should also be deleted.

## Repair or upgrade

Run `install.bat` again. If the script edition is already installed, it asks whether to repair, upgrade, uninstall, or cancel.

Release zips include a `VERSION` file generated from the GitHub release version. The installer copies that value into `%LOCALAPPDATA%\rdpfui-script\VERSION` and uses it for upgrade/repair prompts.

## Config

After install, edit:

```text
%LOCALAPPDATA%\rdpfui-script\config.json
```

Example folder entry:

```json
{
  "path": "%USERPROFILE%\\Downloads",
  "recursive": false,
  "allowWithoutRegex": false,
  "contentRegex": [
    "^full address:s:.*\\.example\\.com(:\\d+)?$"
  ]
}
```

`contentRegex` is matched against the full `.rdp` file contents. If a folder has no regex, files are rejected unless either the folder sets `allowWithoutRegex` to `true` or the global `allowFoldersWithoutRegex` setting is `true`.

## Safety

The watcher only processes files in configured folders and only files with the `.rdp` extension. By default, it also:

- requires a content regex match for every folder
- rejects files above `maxFileSizeKB`
- rejects configured risky RDP directives such as drive, printer, clipboard, COM port, POS device, smart card, and microphone redirection
- moves accepted files to `%LOCALAPPDATA%\rdpfui-script\staging`
- opens files by calling `mstsc.exe` directly

Logs are written to:

```text
%LOCALAPPDATA%\rdpfui-script\logs
```
