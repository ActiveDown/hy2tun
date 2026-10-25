Hy2Tun 0.7.1 is about **finding out why a connection misbehaves**, plus one Windows fix found on the way.

## What's new

- **Save diagnostics** (Log tab): one click writes a `.zip` file and shows its path (on Windows Explorer opens with the file
  selected; a "Download a copy" button is there too). It contains the service logs, the routing settings, the server list
  **without passwords**, the network state of the machine (adapters, routes, DNS, proxies, VPN software, firewall and
  kill-switch filters, recent system events) and a **live self-test**: a few sites (GitHub, Gemini, Google, Cloudflare, ...)
  are tried over system DNS, through the tunnel, directly and over the OS route, so it is visible which step breaks.
- Command line: `hy2tun diag [file.zip]` does the same (to the Desktop by default); it works without the service too.
- **Detailed logging** switch (Log tab): logs every connection with the program and the path chosen. Takes effect on the
  next connect. The log file is now capped (8 MB, then `hy2tun.log.old`).
- Failed connections are always logged (rate limited) and a **health line** is written about once a minute while connected,
  so a tunnel that goes bad leaves a trace even without detailed logging.
- **Windows fix**: the tunnel routes (`0.0.0.0/1`, `128.0.0.0/1`, `::/1`, `8000::/1`) were stored as *persistent* routes and
  one more set piled up in the registry on every connect. They are now active-only and the leftovers are removed on connect.
- Windows diagnostics read console output in the right code page (Cyrillic).

## For someone who has a problem

1. Log tab → switch **Detailed logging** on, disconnect and connect again.
2. Use the connection until the problem shows up (sites stop opening).
3. Log tab → **Save diagnostics** → send the `.zip` file (kept in `C:\ProgramData\Hy2Tun\diagnostics` on Windows,
   `/Library/Application Support/Hy2Tun/diagnostics` on macOS).

The logs and the decision list can contain the names of the sites visited while detailed logging was on.

## Downloads

| File | What |
|------|------|
| `Hy2Tun-0.7.1-setup-windows-amd64.exe` / `-portable-windows-amd64.zip` | Windows installer and portable build |
| `Hy2Tun-0.7.1-macos-installer.pkg` | macOS installer: app + background service |
| `Hy2Tun-0.7.1-macos-app.zip`, `-macos-universal.tar.gz` | `Hy2Tun.app` only / the bare binary |
| `SHA256SUMS.txt`, `SHA256SUMS-macos.txt` | Checksums |

Known limitations are the same as in 0.7.0: the macOS build is not notarized, do not run another system-wide proxy/TUN
(NekoBox, Clash, ...) together with Hy2Tun, kill switch and DNS leak protection exist on Windows only.

Licensed under the [MIT License](https://github.com/ActiveDown/hy2tun/blob/main/LICENSE).
