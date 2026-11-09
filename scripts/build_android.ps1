<#
.SYNOPSIS
  Builds Android JNI libraries and an APK on Windows with the Android SDK/NDK.
.EXAMPLE
  .\scripts\build_android.ps1 -Version 1.2.1 -AndroidSdk C:\Android\Sdk -Jdk C:\Java\jdk-21
.NOTES
  Uses android/keystore.properties for release signing when present. Otherwise
  produces an explicitly named debug APK; no keys or credentials enter Git.
#>
param(
    [string] $Version = '1.2.1',
    [string] $OutDir = 'dist',
    [string] $AndroidSdk = $env:ANDROID_HOME,
    [string] $AndroidNdk = $env:ANDROID_NDK_HOME,
    [string] $Jdk = $env:JAVA_HOME
)

$ErrorActionPreference = 'Stop'
if ($Version -notmatch '^\d+\.\d+\.\d+([.-][A-Za-z0-9.-]+)?$') { throw 'Invalid release version' }
if (-not $AndroidSdk) { $AndroidSdk = $env:ANDROID_SDK_ROOT }
if (-not $AndroidSdk) { $AndroidSdk = Join-Path $env:LOCALAPPDATA 'Android\Sdk' }
if (-not $AndroidNdk) { $AndroidNdk = Join-Path $AndroidSdk 'ndk\27.0.12077973' }
if (-not $Jdk -or -not (Test-Path (Join-Path $Jdk 'bin\java.exe'))) { throw 'Set JAVA_HOME or pass -Jdk (JDK 17+)' }
$clang = Join-Path $AndroidNdk 'toolchains\llvm\prebuilt\windows-x86_64\bin\clang.exe'
if (-not (Test-Path $clang)) { throw "Android NDK clang not found: $clang" }

$root = Split-Path -Parent $PSScriptRoot
$out = if ([IO.Path]::IsPathRooted($OutDir)) { $OutDir } else { Join-Path $root $OutDir }
New-Item -ItemType Directory -Force $out | Out-Null
$saved = @{}
foreach ($name in @('JAVA_HOME', 'ANDROID_HOME', 'PATH', 'CGO_ENABLED', 'GOOS', 'GOARCH', 'GOARM', 'CC')) {
    $saved[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
}
$location = Get-Location
try {
    Set-Location $root
    $env:JAVA_HOME = $Jdk
    $env:ANDROID_HOME = $AndroidSdk
    $env:PATH = "$(Join-Path $Jdk 'bin');$env:PATH"
    $env:CGO_ENABLED = '1'
    $env:GOOS = 'android'
    $ld = "-s -w -X main.appVersion=$Version '-extldflags=-Wl,-soname,libhy2tun.so -Wl,-z,max-page-size=16384'"
    $targets = @(
        @{ ABI = 'arm64-v8a'; Arch = 'arm64'; Target = 'aarch64-linux-android26'; Arm = '' },
        @{ ABI = 'armeabi-v7a'; Arch = 'arm'; Target = 'armv7a-linux-androideabi26'; Arm = '7' },
        @{ ABI = 'x86_64'; Arch = 'amd64'; Target = 'x86_64-linux-android26'; Arm = '' }
    )
    foreach ($target in $targets) {
        Write-Host "Building libhy2tun.so for $($target.ABI)..."
        $env:GOARCH = $target.Arch
        $env:GOARM = $target.Arm
        # clang's .cmd wrappers cannot be used as Go's CC on Windows.
        $env:CC = '"' + $clang + '" --target=' + $target.Target
        $libDir = Join-Path $root "android\app\src\main\jniLibs\$($target.ABI)"
        New-Item -ItemType Directory -Force $libDir | Out-Null
        go build -buildmode=c-shared -trimpath -ldflags $ld -o (Join-Path $libDir 'libhy2tun.so') .
        if ($LASTEXITCODE -ne 0) { throw "Go Android build failed for $($target.ABI)" }
        $header = Join-Path $libDir 'libhy2tun.h'
        if (Test-Path $header) { Remove-Item $header }
    }
    $variant = if (Test-Path 'android\keystore.properties') { 'release' } else { 'debug' }
    $task = if ($variant -eq 'release') { 'assembleRelease' } else { 'assembleDebug' }
    & '.\android\gradlew.bat' -p android --no-daemon --console=plain $task "-PversionName=$Version"
    if ($LASTEXITCODE -ne 0) { throw 'Gradle APK build failed' }
    $suffix = if ($variant -eq 'debug') { '-debug' } else { '' }
    $apk = Join-Path $out "Hy2Tun-$Version-android$suffix.apk"
    Copy-Item "android\app\build\outputs\apk\$variant\app-$variant.apk" $apk -Force
    $sum = '{0}  {1}' -f (Get-FileHash $apk -Algorithm SHA256).Hash.ToLower(), (Split-Path $apk -Leaf)
    [IO.File]::WriteAllText((Join-Path $out 'SHA256SUMS-android.txt'), "$sum`n", [Text.UTF8Encoding]::new($false))
    Write-Host "Done: $apk"
    Write-Host $sum
} finally {
    foreach ($name in $saved.Keys) { [Environment]::SetEnvironmentVariable($name, $saved[$name], 'Process') }
    Set-Location $location
}
