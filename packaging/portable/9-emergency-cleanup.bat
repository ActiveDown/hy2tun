@echo off
rem Use only if the internet is broken after Hy2Tun crashed or was killed (portable mode).
rem Optional: pass the server IP to remove the leftover host route:  9-emergency-cleanup.bat 203.0.113.10
net session >nul 2>&1
if %errorlevel% neq 0 (
  powershell -NoProfile -Command "Start-Process -FilePath '%~f0' -ArgumentList '%1' -Verb RunAs"
  exit /b
)
taskkill /f /im hy2tun.exe >nul 2>&1
if not "%~1"=="" route delete %~1 >nul 2>&1
route delete 0.0.0.0 mask 128.0.0.0 198.18.77.2 >nul 2>&1
route delete 128.0.0.0 mask 128.0.0.0 198.18.77.2 >nul 2>&1
echo Done. The WFP rules and the Hy2Tun adapter disappear when the process ends.
echo If the internet is still down, reboot - nothing is persistent.
pause
