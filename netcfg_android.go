package main

import (
	"net"
	"strconv"
	"strings"
	"sync"
)

// The VPN interface addresses; the app passes the same ones to
// VpnService.Builder (see tunInfo in mobile_android.go).
const (
	tunV4Addr   = "198.18.77.1"
	tunV4Prefix = 30
	tunV6Addr   = "fdfe:dcba:9876::1"
	tunV6Prefix = 126
)

// The app runs as an ordinary user: VpnService gives it the TUN, routes and DNS.
func isAdmin() bool { return true }

type physRoute struct {
	IfIndex string
	NextHop string
	Desc    string
}

// physNet is the underlying (non-VPN) network as reported by the app's
// ConnectivityManager callback. gen grows with every change of the default
// network; it plays the role of the interface index of the desktop builds, so
// Engine.watch notices a switch (Wi-Fi <-> mobile) and refreshes the client.
var physNet = struct {
	mu   sync.Mutex
	gen  uint32
	name string
	dns  []string
	v6   bool
}{gen: 1}

func setPhysNet(name string, dns []string, v6 bool) {
	physNet.mu.Lock()
	defer physNet.mu.Unlock()
	if name != physNet.name || v6 != physNet.v6 || strings.Join(dns, ",") != strings.Join(physNet.dns, ",") {
		physNet.gen++
	}
	physNet.name, physNet.dns, physNet.v6 = name, dns, v6
}

func currentRoute() *physRoute {
	physNet.mu.Lock()
	defer physNet.mu.Unlock()
	return &physRoute{IfIndex: strconv.Itoa(int(physNet.gen)), Desc: physNet.name}
}

// bestRoute: sockets of the app itself never enter the VPN, so the server is
// always reached over the current underlying network.
func bestRoute(ip net.IP) (*physRoute, error) { return currentRoute(), nil }

func hasGlobalV6(ifIndex string) bool {
	physNet.mu.Lock()
	defer physNet.mu.Unlock()
	return physNet.v6
}

// systemDNSServers returns the resolvers of the underlying network.
func systemDNSServers(skip []string) []string {
	physNet.mu.Lock()
	defer physNet.mu.Unlock()
	var out []string
	for _, s := range physNet.dns {
		dup := false
		for _, k := range skip {
			dup = dup || k == s
		}
		if !dup {
			out = append(out, s)
		}
	}
	return out
}

// netConfig has nothing to do on Android: VpnService.Builder sets the address,
// routes and DNS of the interface, and they go away with it.
type netConfig struct {
	name     string
	serverIP net.IP
	hostGW   *physRoute
}

func (n *netConfig) apply(dnsServers []string) error { return nil }
func (n *netConfig) revert()                         {}
