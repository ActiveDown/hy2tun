<#
.SYNOPSIS
  Builds Hy2Tun release artifacts: installer, portable zip and checksums.

.DESCRIPTION
  1. makes sure wintun.dll (amd64) is present and matches the pinned hash
  2. builds the GUI/service exe, the console exe and the installer
  3. optionally Authenticode-signs everything (before the exe is embedded in the
     installer, and the installer itself)
  4. packs the portable zip and writes SHA256SUMS.txt

.EXAMPLE
  .\scripts\build.ps1 -Version 0.5.0
  .\scripts\build.ps1 -Version 0.5.0 -Pfx .\codesign.pfx     # password in $env:SIGN_PFX_PASSWORD
#>
param(
    [Parameter(Mandatory = $true)] [string] $Version,
    [string] $OutDir = 'dist',
    [string] $Pfx,
    [string] $Thumbprint,
    [string] $WintunDll
)

$ErrorActionPreference = 'Stop'
Set-Location (Split-Path -Parent $PSScriptRoot)
$root = (Get-Location).Path
$out = Join-Path $root $OutDir
New-Item -ItemType Directory -Force $out | Out-Null

# --- wintun.dll -------------------------------------------------------------
$wintunZip = 'https://www.wintun.net/builds/wintun-0.14.1.zip'
$wintunSha = 'E5DA8447DC2C320EDC0FC52FA01885C103DE8C118481F683643CACC3220DAFCE' # bin\amd64\wintun.dll
if (-not $WintunDll) {
    $WintunDll = Join-Path $out 'wintun.dll'
    if (-not (Test-Path $WintunDll)) {
        $tmp = Join-Path $out 'wintun.zip'
        Invoke-WebRequest -Uri $wintunZip -OutFile $tmp -UseBasicParsing
        Expand-Archive $tmp -DestinationPath (Join-Path $out 'wintun') -Force
        Copy-Item (Join-Path $out 'wintun\wintun\bin\amd64\wintun.dll') $WintunDll
    }
}
$h = (Get-FileHash $WintunDll -Algorithm SHA256).Hash
if ($h -ne $wintunSha) { throw "wintun.dll hash mismatch: $h" }

$sign = {
    param($files)
    if ($Pfx -or $Thumbprint) {
        $a = @{ Files = $files }
        if ($Pfx) { $a.Pfx = $Pfx } else { $a.Thumbprint = $Thumbprint }
        & (Join-Path $PSScriptRoot 'sign.ps1') @a
    } else {
        Write-Host 'NOTE: not signing (no -Pfx / -Thumbprint): Windows will show "unknown publisher".'
    }
}

# --- binaries ---------------------------------------------------------------
$ld = "-s -w -X main.appVersion=$Version"
$gui = Join-Path $out 'Hy2Tun.exe'
# NOTE: Windows file names are case-insensitive: the console build must not be called hy2tun.exe next to Hy2Tun.exe
$con = Join-Path $out 'hy2tun-console.exe'
go build -trimpath -ldflags "$ld -H=windowsgui" -o $gui .
go build -trimpath -ldflags $ld -o $con .
function Get-Subsystem($f) {
    $b = [IO.File]::ReadAllBytes($f)
    $pe = [BitConverter]::ToInt32($b, 0x3C)
    [BitConverter]::ToUInt16($b, $pe + 24 + 68)
}
if ((Get-Subsystem $gui) -ne 2) { throw "$gui is not a GUI-subsystem executable" }
if ((Get-Subsystem $con) -ne 3) { throw "$con is not a console executable" }
& $sign @($gui, $con)

$payload = Join-Path $root 'cmd\setup\payload'
Copy-Item $gui (Join-Path $payload 'Hy2Tun.exe') -Force
Copy-Item $WintunDll (Join-Path $payload 'wintun.dll') -Force
$setup = Join-Path $out "Hy2Tun-$Version-setup-windows-amd64.exe"
go build -trimpath -ldflags "$ld -H=windowsgui" -o $setup .\cmd\setup
& $sign @($setup)

# --- portable zip -----------------------------------------------------------
$stage = Join-Path $out 'portable'
if (Test-Path $stage) { Get-ChildItem $stage -Recurse | Remove-Item -Recurse -Force }
New-Item -ItemType Directory -Force $stage | Out-Null
Copy-Item (Join-Path $root 'packaging\portable\*') $stage
Copy-Item $con (Join-Path $stage 'hy2tun.exe')
Copy-Item $WintunDll $stage
$zip = Join-Path $out "Hy2Tun-$Version-portable-windows-amd64.zip"
if (Test-Path $zip) { [IO.File]::Delete($zip) }
Compress-Archive -Path (Join-Path $stage '*') -DestinationPath $zip

# --- checksums --------------------------------------------------------------
$sums = foreach ($f in $setup, $zip) {
    '{0}  {1}' -f (Get-FileHash $f -Algorithm SHA256).Hash.ToLower(), (Split-Path $f -Leaf)
}
[IO.File]::WriteAllLines((Join-Path $out 'SHA256SUMS.txt'), $sums)
$sums
