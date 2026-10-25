Hy2Tun 0.7.0 is the first release with a working **macOS** client: a native window, a menu-bar icon, a background service and an installer.

## What's new

- **macOS app and installer**: `Hy2Tun-0.7.0-macos-installer.pkg` installs `Hy2Tun.app` into `/Applications` and sets up the
  launchd service (`com.activedown.hy2tun`) in one step. The app is universal (Apple Silicon + Intel).
- **Per-app rules on macOS**: Safari is attributed correctly (its traffic comes from a WebKit helper), and the Rules page can
  **choose from the installed applications** as well as from the running ones.
- **Apps that always go direct**: a new list of applications that never enter the tunnel in any mode (remote desktops such as
  Windows App, your own tools).
- **Other VPN clients keep working**: WireGuard, OpenVPN Connect, AmneziaVPN, V2Box, Tailscale and ZeroTier on macOS are sent
  straight out, and the tunnel's DNS no longer overrides the DNS of a connected corporate VPN (internal host names resolve).
- **Direct traffic under the tunnel**: direct flows (selected/excluded apps, RU addresses and names) use an interface-scoped
  default route, so they keep working while the tunnel holds the default routes.
- The window follows the service automatically (install, restart, update), offers to update an outdated service and no longer
  drops its connection to the panel; the server drop-down no longer closes while you choose.
- The mac TUN device now gets the 4-byte packet header the utun driver requires.

## Downloads

| File | What |
|------|------|
| `Hy2Tun-0.7.0-macos-installer.pkg` | macOS installer: app + background service (recommended) |
| `Hy2Tun-0.7.0-macos-app.zip` | `Hy2Tun.app` only (the service is installed on the first connect) |
| `Hy2Tun-0.7.0-macos-universal.tar.gz` | the bare `hy2tun` binary for the command line |
| `Hy2Tun-0.7.0-setup-windows-amd64.exe` / `-portable-windows-amd64.zip` | Windows installer and portable build |
| `SHA256SUMS-macos.txt`, `SHA256SUMS.txt` | Checksums |

Requirements (macOS): macOS 11 or newer, an administrator password or Touch ID to install the service.

## Known limitations

- The macOS build is **not notarized** (ad-hoc signed): open the installer with right click → Open, or allow it in
  System Settings → Privacy & Security.
- Do not run another system-wide proxy or TUN (NekoBox/NekoRay, Clash, ...) together with Hy2Tun: their routes and the
  system proxy setting conflict with the tunnel. Switch off their "System proxy" before quitting them.
- OpenVPN Connect is on the always-direct list but was not tested end to end; WireGuard was.
- Kill switch and DNS leak protection are Windows-only for now.
- QUIC (UDP/443) from applications is dropped on purpose so browsers fall back to TCP.

Uninstall on macOS: `sudo /usr/local/bin/hy2tun uninstall`, then delete `/Applications/Hy2Tun.app`.

Licensed under the [MIT License](https://github.com/ActiveDown/hy2tun/blob/main/LICENSE).
