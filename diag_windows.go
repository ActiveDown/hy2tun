//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/sys/windows"
)

var procGetOEMCP = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetOEMCP")

// consoleText turns the output of a console program (OEM code page: ipconfig,
// route, netsh) into UTF-8; PowerShell output is UTF-8 already.
func consoleText(b []byte) string {
	if len(b) == 0 || utf8.Valid(b) {
		return string(b)
	}
	cp, _, _ := procGetOEMCP.Call()
	n, err := windows.MultiByteToWideChar(uint32(cp), 0, &b[0], int32(len(b)), nil, 0)
	if err != nil || n <= 0 {
		return string(b)
	}
	buf := make([]uint16, n)
	if _, err := windows.MultiByteToWideChar(uint32(cp), 0, &b[0], int32(len(b)), &buf[0], n); err != nil {
		return string(b)
	}
	return string(utf16.Decode(buf))
}

func psCmd(name, script string) diagCmd {
	return diagCmd{name: name, argv: []string{"powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script}}
}

const vpnWords = `vpn|wireguard|openvpn|nekoray|nekobox|v2ray|xray|clash|sing-box|hysteria|tailscale|zerotier|hamachi|proxifier|nord|express|proton|cisco|forti|checkpoint|sophos|kaspersky|eset|adguard|outline|amnezia|warp|windscribe|surfshark`

// diagCommands is the Windows network snapshot.
func diagCommands() []diagCmd {
	return []diagCmd{
		{"ipconfig", []string{"ipconfig", "/all"}},
		{"route-print", []string{"route", "print"}},
		{"interfaces-v4", []string{"netsh", "interface", "ipv4", "show", "interfaces"}},
		{"interfaces-v6", []string{"netsh", "interface", "ipv6", "show", "interfaces"}},
		{"subinterfaces-mtu", []string{"netsh", "interface", "ipv4", "show", "subinterfaces"}},
		{"dnsservers", []string{"netsh", "interface", "ipv4", "show", "dnsservers"}},
		{"tcp-global", []string{"netsh", "interface", "tcp", "show", "global"}},
		{"winhttp-proxy", []string{"netsh", "winhttp", "show", "proxy"}},
		{"ie-proxy", []string{"reg", "query", `HKCU\Software\Microsoft\Windows\CurrentVersion\Internet Settings`}},
		{"advfirewall-state", []string{"netsh", "advfirewall", "show", "allprofiles", "state"}},
		{"winsock-catalog", []string{"netsh", "winsock", "show", "catalog"}},
		{"service-hy2tun", []string{"sc", "qc", "Hy2Tun"}},
		{"service-hy2tun-state", []string{"sc", "query", "Hy2Tun"}},
		psCmd("os", `Get-CimInstance Win32_OperatingSystem | Format-List Caption,Version,BuildNumber,LastBootUpTime,OSArchitecture`),
		psCmd("adapters", `Get-NetAdapter -IncludeHidden | Sort-Object ifIndex | Format-Table -AutoSize ifIndex,Name,InterfaceDescription,Status,MtuSize,LinkSpeed,Virtual,Hidden`),
		psCmd("ip-interfaces", `Get-NetIPInterface | Sort-Object InterfaceMetric | Format-Table -AutoSize ifIndex,InterfaceAlias,AddressFamily,NlMtu,InterfaceMetric,ConnectionState,Dhcp`),
		psCmd("dns-client", `Get-DnsClientServerAddress | Format-Table -AutoSize InterfaceAlias,AddressFamily,ServerAddresses`),
		psCmd("dns-nrpt", `Get-DnsClientNrptPolicy | Format-List`),
		psCmd("dns-cache-sample", `Get-DnsClientCache | Select-Object -First 60 | Format-Table -AutoSize Entry,Type,Status,Data`),
		psCmd("antivirus", `Get-CimInstance -Namespace root/SecurityCenter2 -ClassName AntiVirusProduct | Format-List displayName,productState`),
		psCmd("vpn-services", fmt.Sprintf(`Get-Service | Where-Object { $_.Name -match '%s' -or $_.DisplayName -match '%s' } | Format-Table -AutoSize Name,Status,DisplayName`, vpnWords, vpnWords)),
		psCmd("vpn-processes", fmt.Sprintf(`Get-Process | Where-Object { $_.ProcessName -match '%s' } | Format-Table -AutoSize ProcessName,Id,Path`, vpnWords)),
		psCmd("vpn-adapters-drivers", fmt.Sprintf(`Get-CimInstance Win32_PnPSignedDriver | Where-Object { $_.DeviceName -match '%s|tap|tun|wintun|ndis' } | Format-Table -AutoSize DeviceName,DriverVersion,Manufacturer`, vpnWords)),
		psCmd("netstat", `netstat -ano | Select-Object -First 400`),
		psCmd("events-service", `Get-WinEvent -FilterHashtable @{LogName='System'; StartTime=(Get-Date).AddDays(-3)} -MaxEvents 400 -ErrorAction SilentlyContinue | Where-Object { $_.Message -match 'Hy2Tun|wintun|WireGuard|OpenVPN' } | Select-Object -First 40 | Format-List TimeCreated,ProviderName,Id,Message`),
		psCmd("events-app", `Get-WinEvent -FilterHashtable @{LogName='Application'; StartTime=(Get-Date).AddDays(-3)} -MaxEvents 600 -ErrorAction SilentlyContinue | Where-Object { $_.Message -match 'Hy2Tun|hy2tun' } | Select-Object -First 30 | Format-List TimeCreated,ProviderName,Id,Message`),
		psCmd("hosts", `Get-Content "$env:windir\System32\drivers\etc\hosts"`),
		{"wfp-hy2tun", []string{"hy2tun-wfp"}}, // handled in diagRun
	}
}

// diagRun runs a command with a timeout and returns its output (errors included).
func diagRun(timeout time.Duration, argv []string) string {
	if len(argv) == 1 && argv[0] == "hy2tun-wfp" {
		return wfpSnapshot()
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	s := consoleText(out)
	if err != nil {
		s += fmt.Sprintf("\n[error: %v]\n", err)
	}
	return s
}

// wfpSnapshot lists Hy2Tun's own WFP filters (DNS leak block, kill switch), which
// are invisible in the Windows Firewall UI.
func wfpSnapshot() string {
	tmp := filepath.Join(os.TempDir(), fmt.Sprintf("hy2tun-wfp-%d.xml", time.Now().UnixNano()))
	defer os.Remove(tmp)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "netsh", "wfp", "show", "filters", "file="+tmp)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Sprintf("netsh wfp failed (needs administrator): %v\n%s", err, out)
	}
	b, err := os.ReadFile(tmp)
	if err != nil {
		return err.Error()
	}
	items := strings.Split(string(b), "<item>")
	var out strings.Builder
	n := 0
	for _, it := range items {
		if strings.Contains(it, "Hy2Tun") {
			n++
			if n <= 60 {
				if len(it) > 2500 {
					it = it[:2500]
				}
				out.WriteString("<item>" + it + "\n")
			}
		}
	}
	return fmt.Sprintf("filters in total: %d, Hy2Tun filters: %d\n\n%s", len(items)-1, n, out.String())
}
