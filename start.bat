@echo off
setlocal
rem Keep this Windows batch entry point encoded with CRLF line endings.
cd /d "%~dp0"
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0start.ps1" %*
if errorlevel 1 (
  echo.
  echo cc-hardness 启动失败，请查看上方错误信息。
  pause
)
endlocal
