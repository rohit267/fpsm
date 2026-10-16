# Builds the release files into dist\: fpsm.exe, a zip with README and LICENSE, and SHA256SUMS.txt.
#   scripts\build.ps1 -Version v0.1.0
param(
    [string]$Version = 'dev'
)
$ErrorActionPreference = 'Stop'
Set-Location (Split-Path $PSScriptRoot -Parent)

if (Test-Path dist) { Remove-Item dist -Recurse -Force }
New-Item -ItemType Directory dist | Out-Null

$env:CGO_ENABLED = '0'
go build -trimpath -ldflags "-s -w -H=windowsgui -X main.version=$Version" -o dist\fpsm.exe .
if ($LASTEXITCODE -ne 0) { throw "go build failed (exit $LASTEXITCODE)" }

$zip = "dist\fpsm-$Version-windows-x64.zip"
Compress-Archive -Path dist\fpsm.exe, README.md, LICENSE -DestinationPath $zip

Get-FileHash dist\fpsm.exe, $zip -Algorithm SHA256 |
    ForEach-Object { '{0}  {1}' -f $_.Hash.ToLower(), (Split-Path $_.Path -Leaf) } |
    Set-Content dist\SHA256SUMS.txt -Encoding ascii
Get-ChildItem dist | Select-Object Name, Length
