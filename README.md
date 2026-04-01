# rdpfui

`rdpfui` (rdp + [pfui](https://www.berndeutsch.ch/words/55582)) is a lightweight Go tray app that watches one or more folders for new `.rdp` files, moves them into a temporary folder, and opens them automatically.


## Features

- Watch multiple folders
- Recursive folder scanning
- System tray support
- Autostart support on macOS, Linux, and Windows
- Optional per-folder regex filter to only process `.rdp` files whose contents match the pattern
- Moves new `.rdp` files to a temporary user location before opening

## Build

```bash
cd /Users/eliasfrehner/Downloads/rdpfui
go build -o rdpfui
```

## Run

```bash
./rdpfui
```

## Icon

A built-in SVG tray icon is embedded automatically. If you want to replace it, update `resources/icon.svg` and recompile.

## Config

Config is stored in the user config directory:

- macOS: `~/Library/Application Support/rdpfui/config.json`
- Linux: `~/.config/rdpfui/config.json`
- Windows: `%APPDATA%\\rdpfui\\config.json`

You can also add an optional `regex` field per watched folder in the saved config to only process `.rdp` files whose contents match that expression.

## Notes

- On macOS, autostart creates a LaunchAgent plist at `~/Library/LaunchAgents/ch.rdpfui.autostart.plist`.
- The app hides to the tray when the window is closed.
