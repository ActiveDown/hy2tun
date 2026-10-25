//go:build darwin

package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

const (
	tunV4Addr = "198.18.77.1"
	tunV4Mask = "255.255.255.252"
	tunV4GW   = "198.18.77.2"
	tunV6Addr = "fdfe:dcba:9876::1"
	tunV6GW   = "fdfe:dcba:9876::2"
)

func isAdmin() bool {
	return os.Geteuid() == 0
}

func run(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

type physRoute struct {
	IfIndex string
	NextHop string
	Desc    string
}

// bestRoute asks Darwin which interface and gateway would be used to reach ip.
func bestRoute(ip net.IP) (*physRoute, error) {
	out, err := run("route", "-n", "get", ip.String())
	if err != nil {
		return nil, fmt.Errorf("route get %s: %v: %s", ip, err, out)
	}
	var gateway, ifName string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "gateway:") {
			gateway = strings.TrimSpace(strings.TrimPrefix(line, "gateway:"))
		} else if strings.HasPrefix(line, "interface:") {
			ifName = strings.TrimSpace(strings.TrimPrefix(line, "interface:"))
		}
	}
	if ifName == "" {
		return nil, fmt.Errorf("cannot parse interface from route get: %s", out)
	}
	iface, err := net.InterfaceByName(ifName)
	if err != nil {
		return nil, fmt.Errorf("interface %s: %v", ifName, err)
	}
	return &physRoute{
		IfIndex: strconv.Itoa(iface.Index),
		NextHop: gateway,
		Desc:    ifName,
	}, nil
}

// hasGlobalV6 reports whether the given adapter has a non-link-local IPv6 address.
func hasGlobalV6(ifIndex string) bool {
	idx, err := strconv.Atoi(ifIndex)
	if err != nil {
		return false
	}
	iface, err := net.InterfaceByIndex(idx)
	if err != nil {
		return false
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return false
	}
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip != nil && ip.To4() == nil && ip.IsGlobalUnicast() {
			return true
		}
	}
	return false
}

type netConfig struct {
	name       string
	serverIP   net.IP
	hostGW     *physRoute
	applied    bool
	dnsServers []string
}

func (n *netConfig) apply(dnsServers []string) error {
	n.dnsServers = dnsServers

	// 1. Assign IPv4 to the utun interface (point-to-point)
	if out, err := run("ifconfig", n.name, tunV4Addr, tunV4GW, "netmask", tunV4Mask, "up"); err != nil {
		return fmt.Errorf("ifconfig ipv4: %v: %s", err, out)
	}

	// 2. Assign IPv6
	if out, err := run("ifconfig", n.name, "inet6", tunV6Addr, tunV6GW, "prefixlen", "126", "up"); err != nil {
		log.Printf("warn: ifconfig ipv6: %v: %s", err, out)
	}

	// 3. Keep the path to Hysteria2 server outside of the tunnel
	if n.hostGW != nil && n.serverIP.To4() != nil {
		run("route", "delete", "-host", n.serverIP.String())
		var args []string
		if n.hostGW.NextHop != "" && n.hostGW.NextHop != "0.0.0.0" {
			args = []string{"add", "-host", n.serverIP.String(), n.hostGW.NextHop}
		} else if n.hostGW.Desc != "" {
			args = []string{"add", "-host", n.serverIP.String(), "-interface", n.hostGW.Desc}
		}
		if len(args) > 0 {
			if out, err := run("route", args...); err != nil {
				log.Printf("warn: route add server: %v: %s", err, out)
			}
		}
	}
	n.applied = true

	// 4. Default routes through the tunnel
	routes := [][]string{
		{"add", "-net", "0.0.0.0/1", "-interface", n.name},
		{"add", "-net", "128.0.0.0/1", "-interface", n.name},
		{"add", "-inet6", "-net", "::/1", "-interface", n.name},
		{"add", "-inet6", "-net", "8000::/1", "-interface", n.name},
	}
	for _, r := range routes {
		if out, err := run("route", r...); err != nil {
			log.Printf("warn: route %s: %v: %s", strings.Join(r, " "), err, out)
		}
	}

	// 5. DNS configuration via scutil
	if len(dnsServers) > 0 {
		applyDNS(dnsServers)
	}

	return nil
}

func (n *netConfig) revert() {
	if !n.applied {
		return
	}
	n.applied = false

	// Revert DNS
	revertDNS()

	// Revert default routes
	routes := [][]string{
		{"delete", "-net", "0.0.0.0/1"},
		{"delete", "-net", "128.0.0.0/1"},
		{"delete", "-inet6", "-net", "::/1"},
		{"delete", "-inet6", "-net", "8000::/1"},
	}
	for _, r := range routes {
		run("route", r...)
	}

	// Remove server host route
	if n.serverIP != nil && n.serverIP.To4() != nil {
		run("route", "delete", "-host", n.serverIP.String())
	}

	// Bring interface down
	run("ifconfig", n.name, "down")
}

func applyDNS(servers []string) {
	script := fmt.Sprintf("d.init\nd.add ServerAddresses * %s\nd.add SupplementalMatchDomains * \"\"\nset State:/Network/Service/hy2tun/DNS\nset Setup:/Network/Service/hy2tun/DNS\n", strings.Join(servers, " "))
	cmd := exec.Command("scutil")
	cmd.Stdin = strings.NewReader(script)
	cmd.Run()
}

func revertDNS() {
	script := "remove State:/Network/Service/hy2tun/DNS\nremove Setup:/Network/Service/hy2tun/DNS\n"
	cmd := exec.Command("scutil")
	cmd.Stdin = strings.NewReader(script)
	cmd.Run()
}
