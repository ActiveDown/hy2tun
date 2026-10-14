@echo off
cd /d "%~dp0"
net session >nul 2>&1
if %errorlevel% neq 0 (
  echo Requesting administrator rights...
  powershell -NoProfile -Command "Start-Process -FilePath '%~f0' -Verb RunAs"
  exit /b
)
title Hy2Tun (portable) - Ctrl+C = clean stop
if not exist hy2.url if not exist servers.json (
  echo Put your link into a file named hy2.url next to this script, e.g.
  echo   hy2://password@host:443?sni=example.com#MyServer
  pause
  exit /b 1
)
echo Starting Hy2Tun in portable mode. The control panel opens in the browser.
echo Settings, servers and logs live in this folder. Keep this window open.
echo.
hy2tun.exe run -v
echo.
echo Stopped.
pause
