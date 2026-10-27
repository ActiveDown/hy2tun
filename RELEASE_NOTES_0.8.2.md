Hy2Tun 0.8.2 adds the **kill switch on Android** and fixes the QR scanner.

## Android

- **Kill switch**, the same as on Windows: in the *Only selected apps* mode the selected apps have no internet while the
  VPN is off, the other apps are not affected. The block goes up before the tunnel closes (on Disconnect, on an error),
  so there is no moment in which the apps could leave over the normal network, and it comes back when the app starts.
  While it holds, the notification offers *Connect*. To protect **all** apps, use *Always-on VPN* with *Block
  connections without VPN* in the Android settings — the *Open VPN settings* button under the switch leads there.
- **The QR scanner opened in landscape** and could not be turned; it now opens in portrait.

Windows and macOS have no changes since 0.8.1.

## Downloads

| File | What |
|------|------|
| `Hy2Tun-0.8.2-setup-windows-amd64.exe` / `-portable-windows-amd64.zip` | Windows installer and portable build |
| `Hy2Tun-0.8.2-macos-installer.pkg` | macOS installer: app + background service |
| `Hy2Tun-0.8.2-macos-app.zip`, `-macos-universal.tar.gz` | `Hy2Tun.app` only / the bare binary |
| `Hy2Tun-0.8.2-android.apk` | Android 8.0+ (arm64, armv7, x86_64); installs over 0.8.x |
| `SHA256SUMS.txt`, `SHA256SUMS-macos.txt`, `SHA256SUMS-android.txt` | Checksums |

Licensed under the [MIT License](https://github.com/ActiveDown/hy2tun/blob/main/LICENSE).
