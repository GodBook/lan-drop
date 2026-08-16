@echo off
title LAN Drop - Fast Local Transfer
echo ===================================================
echo   LAN Drop - Starting Local Service...
echo ===================================================
if exist "dist\landrop-windows-amd64.exe" (
    "dist\landrop-windows-amd64.exe"
) else if exist "landrop.exe" (
    "landrop.exe"
) else (
    echo [Info] Compiled binary not found, starting with Python...
    python run_dev_server.py
)
pause
