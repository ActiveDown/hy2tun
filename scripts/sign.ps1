<#
.SYNOPSIS
  Authenticode-signs Hy2Tun binaries.

.DESCRIPTION
  Signs the given files with a code-signing certificate and adds an RFC 3161
  timestamp, so the signature stays valid after the certificate expires.
  The certificate comes from a PFX file or from the Windows certificate store.

  Windows shows the publisher name (and builds SmartScreen reputation) only for
  certificates issued by a CA that Windows trusts. A self-signed certificate
  works technically but is shown as "unknown publisher" on every other machine.

.EXAMPLE
  $env:SIGN_PFX_PASSWORD = '...'
  .\scripts\sign.ps1 -Files .\Hy2Tun.exe, .\Hy2Tun-Setup.exe -Pfx .\codesign.pfx

.EXAMPLE
  .\scripts\sign.ps1 -Files .\Hy2Tun-Setup.exe -Thumbprint 0123ABCD...   # hardware token / store
#>
param(
    [Parameter(Mandatory = $true)] [string[]] $Files,
    [string] $Pfx,
    [string] $PfxPasswordEnv = 'SIGN_PFX_PASSWORD',
    [string] $Thumbprint,
    [string] $TimestampUrl = 'http://timestamp.digicert.com'
)

$ErrorActionPreference = 'Stop'

if ($Pfx) {
    $pw = [Environment]::GetEnvironmentVariable($PfxPasswordEnv)
    if (-not $pw) { throw "Set the password in the environment variable $PfxPasswordEnv" }
    $cert = New-Object System.Security.Cryptography.X509Certificates.X509Certificate2 `
        -ArgumentList (Resolve-Path $Pfx).Path, $pw
} elseif ($Thumbprint) {
    $cert = Get-ChildItem Cert:\CurrentUser\My, Cert:\LocalMachine\My -ErrorAction SilentlyContinue |
        Where-Object { $_.Thumbprint -eq $Thumbprint.ToUpper() } | Select-Object -First 1
    if (-not $cert) { throw "Certificate $Thumbprint not found in the store" }
} else {
    throw 'Pass -Pfx or -Thumbprint'
}

foreach ($f in $Files) {
    $path = (Resolve-Path $f).Path
    $r = Set-AuthenticodeSignature -FilePath $path -Certificate $cert `
        -TimestampServer $TimestampUrl -HashAlgorithm SHA256
    if ($r.Status -notin 'Valid', 'UnknownError') {
        # "UnknownError" is what a not-yet-trusted (e.g. self-signed) chain reports
        throw "Signing $path failed: $($r.Status) $($r.StatusMessage)"
    }
    $v = Get-AuthenticodeSignature -FilePath $path
    '{0}: {1} (signer: {2})' -f (Split-Path $path -Leaf), $v.Status, $v.SignerCertificate.Subject
}
