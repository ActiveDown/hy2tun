Hy2Tun 0.8.1 fixes the layout of the window on narrow screens (Android phones).

## Fixes

- **The page scrolled sideways** on phones: the server picker on the Home tab (with a long server name or the
  "No servers" hint) and the link field on the Servers tab were wider than the screen, so the **Add** button ended up
  off-screen. Fields now shrink with the window and long text is cut off.
- On narrow screens the *Share* / *Delete* buttons of a server go under its name, so the name stays readable.
- No desktop-style scrollbar on touch screens (scrolling works as before).

The window on Windows and macOS looks the same as in 0.8.0.

## Downloads

| File | What |
|------|------|
| `Hy2Tun-0.8.1-setup-windows-amd64.exe` / `-portable-windows-amd64.zip` | Windows installer and portable build |
| `Hy2Tun-0.8.1-macos-installer.pkg` | macOS installer: app + background service |
| `Hy2Tun-0.8.1-macos-app.zip`, `-macos-universal.tar.gz` | `Hy2Tun.app` only / the bare binary |
| `Hy2Tun-0.8.1-android.apk` | Android 8.0+ (arm64, armv7, x86_64); installs over 0.8.0 |
| `SHA256SUMS.txt`, `SHA256SUMS-macos.txt`, `SHA256SUMS-android.txt` | Checksums |

Licensed under the [MIT License](https://github.com/ActiveDown/hy2tun/blob/main/LICENSE).
