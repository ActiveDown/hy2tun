# Hy2Tun

A Windows and macOS client for [Hysteria 2](https://v2.hysteria.network/) with a global TUN, **per-application
routing** and a desktop window in the style of Amnezia. Written in Go on top of the official
`hysteria/core/v2` library.

> **Status: stable release (0.6).** The routing engine, UI, background daemon / Windows service,
> and per-app routing rules have been exercised on Windows 11 and macOS (Apple Silicon & Intel).

## Features

- **Global TUN** (Wintun + a userspace TCP/IP stack): all TCP and UDP, IPv4 and IPv6.
- **Per-process rules** — three modes, chosen in the UI:
  - *All traffic through the VPN*
  - *Only selected apps* (e.g. all of Chrome) — everything else goes direct
  - *Everything except selected apps*
- **Exceptions on top of the apps**: domains (suffix match), IPs and subnets that are
  *always direct* or *always through the tunnel*.
- **Russian resources switch**: `.ru`, `.su`, `.рф`, well-known RU services and (for flows
  without a name) IP addresses allocated to Russia can go direct even inside a selected app.
  The address list comes from the RIPE NCC delegated statistics (`tools/genru`).
- **Other VPN clients go direct** (WireGuard, OpenVPN, Tailscale …), so a second VPN is not
  tunnelled into the first one.
- **Windows service** (automatic, delayed start, restart on failure) + a **desktop window and
  tray icon**; the window needs no administrator rights.
- **Reconnects** after a network change (Wi-Fi ↔ cable, new gateway) and after sleep.
- **DNS leak protection** and an optional **kill switch** for the selected apps, implemented
  with the Windows Filtering Platform (dynamic session — nothing persistent is left behind).
- **Server list** with `hy2://` import, copy / QR code sharing.
- **Obfuscation and port hopping**: `obfs=salamander` (or `gecko`) with `obfs-password`, a port range in the
  address (`host:20000-30000`) or `mport=20000-30000`, `hop-interval=<seconds>`, and `pinSHA256` certificate pinning.
- **English and Russian** UI (English by default).
- Self-contained **installer** (`Hy2Tun-Setup.exe`).

## How routing decides

For every connection, in this order:

1. “Always through the tunnel” list → tunnel
2. Traffic of another VPN client (`wireguard.exe`, …) → direct
3. “Always direct” list → direct
4. Mode + app list (the owner process is found from the Windows connection tables, including
   its ancestors) → tunnel or direct
5. If the result is “tunnel” and *Proxy Russian resources* is off: `.ru` etc. → direct

The domain of a connection comes from the TLS SNI / HTTP `Host` header, and from the DNS answers
that pass through the tunnel. Direct sockets are pinned to the real adapter (`IP_UNICAST_IF`)
so they never loop back into the TUN.

## Build

Requires Go 1.27+.

### macOS

```bash
# Build binary directly:
go build -trimpath -ldflags "-s -w" -o hy2tun .

# Or build a Universal release (Apple Silicon + Intel) with packaging:
./scripts/build_darwin.sh 0.6.0
```

### Windows

```powershell
go build -trimpath -ldflags "-s -w" -o hy2tun.exe .                 # console build (portable / debugging)
go build -trimpath -ldflags "-s -w -H=windowsgui" -o Hy2Tun.exe .   # service + window + tray

# installer: put Hy2Tun.exe and wintun.dll into cmd/setup/payload first
go build -trimpath -ldflags "-s -w -H=windowsgui" -o Hy2Tun-Setup.exe ./cmd/setup
```

Or all at once (downloads and verifies `wintun.dll`, builds, packs, writes checksums into `dist/`):

```powershell
.\scripts\build.ps1 -Version 0.5.0
```

`wintun.dll` (amd64) comes from <https://www.wintun.net/>; it is signed by WireGuard LLC and is
not part of this repository (the build script pins its SHA-256).

### Code signing

Windows shows "unknown publisher" for unsigned programs. To sign the release build you need a
code-signing certificate issued by a CA that Windows trusts:

```powershell
$env:SIGN_PFX_PASSWORD = '...'
.\scripts\build.ps1 -Version 0.5.0 -Pfx .\codesign.pfx        # or: -Thumbprint <token/store cert>
```

The GitHub Actions workflow (`.github/workflows/release.yml`) builds and publishes a release for
every `v*` tag and signs it automatically when the repository secrets `SIGN_PFX_BASE64` and
`SIGN_PFX_PASSWORD` are set. A self-signed certificate works technically, but is shown as an
unknown publisher on every other machine.

### Icon

The icon is generated from code (`internal/logo`, `go run ./tools/genicon`) and embedded with
[rsrc](https://github.com/akavel/rsrc): `rsrc -manifest assets/app.manifest -ico assets/app.ico -arch amd64 -o rsrc_windows_amd64.syso`.

## Run

## Run

Portable (foreground, administrator / root rights needed):

macOS:
```bash
sudo ./hy2tun run -link "hy2://password@host:443?sni=example.com#Name"
```

Windows:
```powershell
hy2tun.exe run -link hy2://password@host:443?sni=example.com#Name
```

The panel address with its access token is printed to the log (default `127.0.0.1:7878`).

macOS service installation (launchd daemon):
```bash
sudo ./hy2tun install
sudo ./hy2tun uninstall
```

Desktop Tray / GUI:
```bash
./hy2tun gui
```

Other commands: `hy2tun service | install | uninstall | gui | api | dry | version`.

## Tests

```bash
go test ./...
```

`TestDirectDial` is skipped unless `HY2_IFINDEX` is set to a physical interface index.

## Project layout

| Path | What |
|------|------|
| `engine.go`, `stack.go`, `flows.go` | tunnel lifecycle, userspace stack, per-flow relay |
| `route.go`, `config.go`, `geoip.go` | routing decision, settings, RU address list |
| `procinfo.go`, `procinfo_windows.go`, `procinfo_darwin.go` | socket → process lookup (Windows API / Darwin libproc) |
| `netcfg_windows.go`, `netcfg_darwin.go` | interface routes, DNS configuration |
| `netwatch_windows.go`, `netwatch_darwin.go` | network-change & sleep detection |
| `direct_windows.go`, `direct_darwin.go` | physical interface direct socket binding |
| `firewall_windows.go`, `firewall_darwin.go` | firewall policy (WFP / packet filter) |
| `service_windows.go`, `service_darwin.go` | Windows service / macOS launchd daemon |
| `gui_windows.go`, `gui_darwin.go` | desktop window + menu bar tray |
| `api.go`, `ui.html` | local control API and the web UI |
| `tools/genru` | builds `ru_cidr.txt` from the RIPE NCC statistics |
| `api.go`, `ui.html` | local control API and the web UI shown in the window |
| `tools/genru` | builds `ru_cidr.txt` from the RIPE NCC statistics |

## Security notes

- The control API listens on `127.0.0.1` only, requires a random token and checks the `Host`
  header. The token file is readable by local users (needed by the window).
- `servers.json` holds the server links (with passwords); in installed mode it is restricted to
  SYSTEM and Administrators. The API never returns a password except for an explicit *Share*.
- The firewall rules live in a dynamic WFP session: they disappear when the process exits.

## Known limitations

- Tested on Windows 10/11 (x64) and macOS 14+ (Apple Silicon & Intel).
- Windows installer is unsigned; macOS requires granting network/root permissions for TUN routes.
- Link parameters that are not supported (for example an unknown `obfs` type) are rejected with an error instead of being ignored.
- The Russian address list is a snapshot; refresh it with `go run ./tools/genru`.
- No code signing, so SmartScreen may warn about the installer.

## License

Hy2Tun is free software under the [MIT License](LICENSE). Contributions are welcome under the
same license.

### Third-party code

All dependencies are permissively licensed and compatible with the MIT License:

- [Hysteria](https://github.com/apernet/hysteria) core — MIT
- [gVisor](https://gvisor.dev/) netstack — Apache-2.0
- [WireGuard](https://www.wireguard.com/) `tun` package and, in `internal/wfp`, parts of
  WireGuard for Windows (WFP bindings) — MIT, see `internal/wfp/LICENSE-wireguard-windows.txt`
- [go-webview2](https://github.com/jchv/go-webview2) — MIT, [fyne systray](https://github.com/fyne-io/systray) — Apache-2.0,
  [go-qrcode](https://github.com/skip2/go-qrcode) — MIT
- [Wintun](https://www.wintun.net/) (`wintun.dll`, downloaded separately) — see its own license terms
- The Russian address list is derived from the public RIPE NCC delegated statistics.
