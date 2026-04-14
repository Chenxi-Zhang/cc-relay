@echo off
cd /d "%~dp0"
cc-relay.exe serve --config config.yaml %*
