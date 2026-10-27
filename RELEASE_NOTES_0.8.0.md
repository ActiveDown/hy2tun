Hy2Tun 0.8.0 adds an **Android version**.

## Android

- The same window as on Windows and macOS: the power button, servers, rules and log, in English and Russian.
- Add a server by pasting a `hy2://` / `hysteria2://` link, by **scanning a QR code**, or by opening such a link on the phone.
- **Per-app routing**: *all apps*, *only the selected apps* or *everything except the selected apps*, picked from the
  installed apps. Changing the list while connected reconnects with the new list.
- *Proxy Russian resources*, *Always direct* and *Always through the tunnel* work as on the desktop, including the
  fallback to the network's own DNS for names only the local network knows.
- Follows network changes (Wi-Fi ↔ mobile data) and reconnects by itself.
- A notification shows the speed and has a *Disconnect* button. The app can be set as the system *Always-on VPN*.
- The app's own connections never enter the tunnel (they are excluded from the VPN and protected with
  `VpnService.protect()`), so there is no routing loop.

Requires Android 8.0 or newer. The APK is distributed here, not through Google Play: allow installing apps from this
source when Android asks.

## Downloads

| File | What |
|------|------|
| `Hy2Tun-0.8.0-setup-windows-amd64.exe` / `-portable-windows-amd64.zip` | Windows installer and portable build |
| `Hy2Tun-0.8.0-macos-installer.pkg` | macOS installer: app + background service |
| `Hy2Tun-0.8.0-macos-app.zip`, `-macos-universal.tar.gz` | `Hy2Tun.app` only / the bare binary |
| `Hy2Tun-0.8.0-android.apk` | Android (arm64, armv7, x86_64) |
| `SHA256SUMS.txt`, `SHA256SUMS-macos.txt`, `SHA256SUMS-android.txt` | Checksums |

Windows and macOS have no functional changes since 0.7.3. Known limitations: the macOS build is not notarized; kill
switch and DNS leak protection exist on Windows only (on Android use *Always-on VPN* with *Block connections without
VPN* in the system settings); do not run another system-wide proxy/TUN together with Hy2Tun.

Licensed under the [MIT License](https://github.com/ActiveDown/hy2tun/blob/main/LICENSE).
