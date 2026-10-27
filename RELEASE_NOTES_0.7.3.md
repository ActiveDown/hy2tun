Hy2Tun 0.7.3 fixes **macOS together with WireGuard / other network-extension VPNs** and corporate DNS names.
(0.7.2 was never published; its changes are included here.)

## Fixes

- **macOS: Safari, Chrome, Claude and other apps lost the internet when WireGuard was connected while Hy2Tun was on**
  (Telegram and the terminal kept working). Hy2Tun published its TUN as a network service that macOS could pick as the
  primary one; a WireGuard tunnel connecting on top of it made macOS refuse every Network.framework connection. The TUN is
  now published with `PrimaryRank = Never`. WireGuard profiles whose `Endpoint` is a host name keep connecting.
- **macOS: names only the local network knows** (corporate hosts, names behind a VPN on the router) no longer fail with
  "unknown host" while Hy2Tun is on. When the tunnel resolver answers "no such name", Hy2Tun asks the network's own DNS
  servers (taken from the system before connecting) over the physical adapter. A "no records of this type" answer from them
  now wins over the public NXDOMAIN, so IPv4-only hosts resolve too.
- Domains in **Always direct** are resolved by the network's own DNS first, then by the RU resolver.

## Downloads

| File | What |
|------|------|
| `Hy2Tun-0.7.3-setup-windows-amd64.exe` / `-portable-windows-amd64.zip` | Windows installer and portable build |
| `Hy2Tun-0.7.3-macos-installer.pkg` | macOS installer: app + background service |
| `Hy2Tun-0.7.3-macos-app.zip`, `-macos-universal.tar.gz` | `Hy2Tun.app` only / the bare binary |
| `SHA256SUMS.txt`, `SHA256SUMS-macos.txt` | Checksums |

Known limitations are the same as in 0.7.0: the macOS build is not notarized, do not run another system-wide proxy/TUN
(NekoBox, Clash, ...) together with Hy2Tun, kill switch and DNS leak protection exist on Windows only. The network-DNS
fallback is macOS only for now.

Licensed under the [MIT License](https://github.com/ActiveDown/hy2tun/blob/main/LICENSE).
