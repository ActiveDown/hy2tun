Hy2Tun 0.6.0 adds the transport features that servers behind DPI most often need: **obfuscation** and **port hopping**.

## What's new

- **Obfuscation**: `obfs=salamander` (and `gecko`) with `obfs-password` in the `hy2://` link, so QUIC is not recognisable by DPI.
- **Port hopping**: a port range in the address (`hy2://pw@host:20000-30000/`) or `mport=20000-30000`;
  the hop period can be set with `hop-interval=<seconds>` (at least 5).
- **Certificate pinning**: `pinSHA256=<fingerprint>` (hex, with or without `:`).
- The server list shows `obfs` and `hop` for such servers.
- The link parser was rewritten: unsupported parameters now produce a clear error instead of being ignored.
- Hopping and the reconnect-after-network-change logic use the already resolved server IP, so they do not depend on DNS.

Existing links keep working unchanged; servers and rules are kept on upgrade.

## Downloads

| File | What |
|------|------|
| `Hy2Tun-0.6.0-setup-windows-amd64.exe` | Installer: service + window + tray + uninstaller (recommended) |
| `Hy2Tun-0.6.0-portable-windows-amd64.zip` | Portable build, no installation (console + browser panel) |
| `SHA256SUMS.txt` | Checksums |

Requirements: Windows 10/11 x64, administrator rights for the service / TUN, Microsoft Edge WebView2 Runtime.

## Known limitations

- The binaries are **not code-signed** yet, so Windows SmartScreen may show "unknown publisher".
- Obfuscation and hopping are covered by automated end-to-end tests against a local Hysteria 2 server;
  hopping over a real multi-port range has not been field-tested yet.
- Kill switch matches applications by executable path (children with another exe are not covered).
- QUIC (UDP/443) from applications is dropped on purpose so browsers fall back to TCP.

Licensed under the [MIT License](https://github.com/ActiveDown/hy2tun/blob/main/LICENSE).
