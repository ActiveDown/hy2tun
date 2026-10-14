Hy2Tun 0.4.0 - portable
=======================

1. Create a file hy2.url next to hy2tun.exe containing your link:
       hy2://password@host:443?sni=example.com#MyServer
2. Turn other VPNs / TUN clients off.
3. Run 1-run-as-admin.bat (UAC). The control panel opens in your browser.
4. Ctrl+C in the console window = clean stop.

Settings, servers and logs are stored in this folder. hy2.url / servers.json contain your
password - do not share this folder.

If the internet is broken after a crash: 9-emergency-cleanup.bat (or reboot).
Project page: https://github.com/ActiveDown/hy2tun
