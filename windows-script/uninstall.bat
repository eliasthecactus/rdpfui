@echo off
setlocal

set "SCRIPT_DIR=%~dp0"
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%SCRIPT_DIR%rdpfui-install.ps1" -Mode Uninstall
exit /b %ERRORLEVEL%
