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
	"time"
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
	scopedIf   string // interface that got our scoped default route ("" = none)
}

// scopedDefaultArgs builds the arguments of `route <cmd>` for the default route
// that is scoped to a physical interface.
func scopedDefaultArgs(cmd string, rt *physRoute) []string {
	args := []string{cmd, "-net", "-ifscope", rt.Desc, "default"}
	if cmd == "add" {
		if ip := net.ParseIP(rt.NextHop); ip != nil && ip.To4() != nil {
			args = append(args, rt.NextHop)
		} else {
			args = append(args, "-interface", rt.Desc)
		}
	}
	return args
}

// addScopedDefault gives sockets bound to the physical interface (IP_BOUND_IF,
// used for direct traffic) a way out. Without it, as soon as the 0/1 and 128/1
// tunnel routes exist, such sockets fail with "network is unreachable".
func (n *netConfig) addScopedDefault(rt *physRoute) {
	n.removeScopedDefault()
	if rt == nil || rt.Desc == "" {
		return
	}
	run("route", scopedDefaultArgs("delete", rt)...)
	if out, err := run("route", scopedDefaultArgs("add", rt)...); err != nil {
		log.Printf("warn: route add scoped default on %s: %v: %s", rt.Desc, err, out)
		return
	}
	n.scopedIf = rt.Desc
}

func (n *netConfig) removeScopedDefault() {
	if n.scopedIf == "" {
		return
	}
	run("route", "delete", "-net", "-ifscope", n.scopedIf, "default")
	n.scopedIf = ""
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

	// Direct (bound to the physical interface) sockets must keep working once the
	// tunnel routes take over.
	n.addScopedDefault(n.hostGW)

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
		applyDNS(dnsServers, n.name)
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

	n.removeScopedDefault()

	// Remove server host route
	if n.serverIP != nil && n.serverIP.To4() != nil {
		run("route", "delete", "-host", n.serverIP.String())
	}

	// Bring interface down
	run("ifconfig", n.name, "down")
}

// scutilSet runs a block of scutil commands.
func scutilSet(script string) {
	cmd := exec.Command("scutil")
	cmd.Stdin = strings.NewReader(script)
	cmd.Run()
}

// scopedResolverFor reports whether the system DNS configuration has a resolver
// scoped to the given interface (the "for scoped queries" section).
func scopedResolverFor(ifName string) bool {
	out, err := exec.Command("scutil", "--dns").Output()
	if err != nil {
		return false
	}
	text := string(out)
	i := strings.Index(text, "for scoped queries")
	if i < 0 {
		return false
	}
	return strings.Contains(text[i:], "("+ifName+")")
}

// applyDNS publishes the tunnel resolvers. macOS network extensions (WireGuard,
// ...) treat the TUN as the primary interface while it holds the default routes
// and resolve their endpoint names scoped to it; without a resolver scoped to the
// TUN that fails instantly ("DNS resolution failed"). InterfaceName asks for such
// a resolver; if configd does not create one from the DNS entry alone, the TUN is
// also published as a service with an address, which it does.
func applyDNS(servers []string, ifName string) {
	scutilSet(fmt.Sprintf("d.init\nd.add ServerAddresses * %s\nd.add SupplementalMatchDomains * \"\"\nd.add InterfaceName %s\nset State:/Network/Service/hy2tun/DNS\nset Setup:/Network/Service/hy2tun/DNS\n",
		strings.Join(servers, " "), ifName))
	if scopedResolverFor(ifName) {
		log.Printf("dns: resolver scoped to %s is active", ifName)
		return
	}
	scutilSet(fmt.Sprintf("d.init\nd.add Addresses * %s\nd.add SubnetMasks * %s\nd.add DestAddresses * %s\nd.add InterfaceName %s\nset State:/Network/Service/hy2tun/IPv4\n",
		tunV4Addr, tunV4Mask, tunV4GW, ifName))
	time.Sleep(300 * time.Millisecond)
	log.Printf("dns: scoped resolver after publishing the TUN service: %v", scopedResolverFor(ifName))
}

func revertDNS() {
	scutilSet("remove State:/Network/Service/hy2tun/DNS\nremove Setup:/Network/Service/hy2tun/DNS\nremove State:/Network/Service/hy2tun/IPv4\n")
}
