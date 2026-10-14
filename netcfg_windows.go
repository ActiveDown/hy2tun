package main

import (
	"fmt"
	"log"
	"net"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

const (
	tunV4Addr = "198.18.77.1"
	tunV4Mask = "255.255.255.252"
	tunV4GW   = "198.18.77.2"
	tunV6Addr = "fdfe:dcba:9876::1/126"
	tunV6GW   = "fdfe:dcba:9876::2"
)

func isAdmin() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

// run executes a command without a console window and returns its combined output.
func run(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func ps(script string) (string, error) {
	return run("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
}

// physRoute is the current best (non-tunnel) route to a destination.
type physRoute struct {
	IfIndex string
	NextHop string
	Desc    string
}

// bestRoute asks Windows which interface and gateway would be used to reach ip.
// It must be called before the tunnel routes are installed.
func bestRoute(ip net.IP) (*physRoute, error) {
	script := fmt.Sprintf(`$r = Find-NetRoute -RemoteIPAddress '%s' | Where-Object { $_.NextHop } | Select-Object -First 1;`+
		` $a = Get-NetAdapter -InterfaceIndex $r.InterfaceIndex -ErrorAction SilentlyContinue;`+
		` "$($r.InterfaceIndex)|$($r.NextHop)|$($a.InterfaceDescription)"`, ip.String())
	out, err := ps(script)
	if err != nil {
		return nil, fmt.Errorf("Find-NetRoute: %v: %s", err, out)
	}
	parts := strings.SplitN(strings.TrimSpace(out), "|", 3)
	if len(parts) < 3 || parts[0] == "" {
		return nil, fmt.Errorf("cannot parse route info: %q", out)
	}
	return &physRoute{IfIndex: parts[0], NextHop: parts[1], Desc: parts[2]}, nil
}

// hasGlobalV6 reports whether the given adapter has a non-link-local IPv6
// address and an IPv6 default route, i.e. can really reach the IPv6 internet.
// Must be called before the tunnel routes are installed.
func hasGlobalV6(ifIndex string) bool {
	script := fmt.Sprintf(`$a = Get-NetIPAddress -AddressFamily IPv6 -InterfaceIndex %s -ErrorAction SilentlyContinue | Where-Object { $_.IPAddress -notlike 'fe80*' -and $_.IPAddress -ne '::1' };`+
		` $r = Get-NetRoute -AddressFamily IPv6 -DestinationPrefix '::/0' -InterfaceIndex %s -ErrorAction SilentlyContinue;`+
		` if ($a -and $r) { 'yes' } else { 'no' }`, ifIndex, ifIndex)
	out, err := ps(script)
	return err == nil && strings.TrimSpace(out) == "yes"
}

// netConfig applies and reverts all OS-level changes made for the tunnel.
type netConfig struct {
	name     string
	serverIP net.IP
	hostGW   *physRoute
	applied  bool
}

func (n *netConfig) hostRouteArgs(verb string) []string {
	args := []string{verb, n.serverIP.String(), "mask", "255.255.255.255"}
	if verb == "add" {
		args = append(args, n.hostGW.NextHop, "metric", "1", "if", n.hostGW.IfIndex)
	}
	return args
}

func (n *netConfig) apply(dnsServers []string) error {
	// wait until the adapter is visible to netsh
	var err error
	for i := 0; i < 20; i++ {
		_, err = run("netsh", "interface", "ipv4", "show", "interface", n.name)
		if err == nil {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if err != nil {
		return fmt.Errorf("adapter %q did not appear", n.name)
	}

	// Keep the path to the Hysteria2 server outside of the tunnel (loop prevention).
	if n.hostGW != nil && n.hostGW.NextHop != "" && n.hostGW.NextHop != "0.0.0.0" && n.serverIP.To4() != nil {
		run("route", n.hostRouteArgs("delete")...) // stale entry from a crashed run
		if out, err := run("route", n.hostRouteArgs("add")...); err != nil {
			return fmt.Errorf("route add server: %v: %s", err, out)
		}
	}
	n.applied = true

	steps := [][]string{
		{"interface", "ipv4", "set", "address", "name=" + n.name, "source=static", "address=" + tunV4Addr, "mask=" + tunV4Mask},
		{"interface", "ipv4", "set", "interface", n.name, "metric=1"},
		{"interface", "ipv4", "add", "route", "0.0.0.0/1", n.name, tunV4GW, "metric=1"},
		{"interface", "ipv4", "add", "route", "128.0.0.0/1", n.name, tunV4GW, "metric=1"},
	}
	for _, s := range steps {
		if out, err := run("netsh", s...); err != nil {
			return fmt.Errorf("netsh %s: %v: %s", strings.Join(s, " "), err, out)
		}
	}
	for i, d := range dnsServers {
		var s []string
		if i == 0 {
			s = []string{"interface", "ipv4", "set", "dnsservers", "name=" + n.name, "source=static", "address=" + d, "validate=no"}
		} else {
			s = []string{"interface", "ipv4", "add", "dnsservers", "name=" + n.name, "address=" + d, fmt.Sprintf("index=%d", i+1), "validate=no"}
		}
		if out, err := run("netsh", s...); err != nil {
			log.Printf("warn: dns %s: %v: %s", d, err, out)
		}
	}

	// IPv6: capture it too, otherwise v6 traffic would bypass the tunnel.
	v6 := [][]string{
		{"interface", "ipv6", "set", "address", "interface=" + n.name, "address=" + tunV6Addr},
		{"interface", "ipv6", "add", "route", "::/1", n.name, tunV6GW, "metric=1"},
		{"interface", "ipv6", "add", "route", "8000::/1", n.name, tunV6GW, "metric=1"},
	}
	for _, s := range v6 {
		if out, err := run("netsh", s...); err != nil {
			log.Printf("warn: ipv6 setup (%s): %v: %s", strings.Join(s, " "), err, out)
			break
		}
	}
	return nil
}

func (n *netConfig) revert() {
	if !n.applied {
		return
	}
	n.applied = false
	if n.hostGW != nil && n.hostGW.NextHop != "" && n.hostGW.NextHop != "0.0.0.0" && n.serverIP.To4() != nil {
		run("route", n.hostRouteArgs("delete")...)
	}
	// Routes bound to the TUN adapter disappear together with the adapter.
}
