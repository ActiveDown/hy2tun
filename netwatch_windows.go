package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var procGetBestInterfaceEx = modIphlpapi.NewProc("GetBestInterfaceEx")

// bestInterfaceFor returns the interface index Windows would use to reach ip.
// This is a cheap syscall, suitable for polling.
func bestInterfaceFor(ip net.IP) (uint32, error) {
	var sa [28]byte
	if v4 := ip.To4(); v4 != nil {
		binary.LittleEndian.PutUint16(sa[0:], 2) // AF_INET
		copy(sa[4:8], v4)
	} else if v6 := ip.To16(); v6 != nil {
		binary.LittleEndian.PutUint16(sa[0:], 23) // AF_INET6
		copy(sa[8:24], v6)
	} else {
		return 0, errors.New("bad ip")
	}
	var idx uint32
	r, _, _ := procGetBestInterfaceEx.Call(uintptr(unsafe.Pointer(&sa[0])), uintptr(unsafe.Pointer(&idx)))
	if r != 0 {
		return 0, windows.Errno(r)
	}
	return idx, nil
}

var virtualAdapter = regexp.MustCompile(`(?i)wintun|wireguard|tap-windows|openvpn|nekoray|nekobox|sing-tun|tailscale|zerotier|hamachi`)

// physicalDefaultRoute finds the best IPv4 default route that does not belong
// to our own TUN adapter or another virtual VPN adapter.
func physicalDefaultRoute(tunAlias string) (*physRoute, error) {
	script := fmt.Sprintf(`Get-NetRoute -AddressFamily IPv4 -DestinationPrefix '0.0.0.0/0' -ErrorAction SilentlyContinue |`+
		` Where-Object { $_.InterfaceAlias -ne '%s' -and $_.NextHop -ne '0.0.0.0' } |`+
		` ForEach-Object {`+
		`   $m = (Get-NetIPInterface -InterfaceIndex $_.ifIndex -AddressFamily IPv4 -ErrorAction SilentlyContinue).InterfaceMetric;`+
		`   $a = Get-NetAdapter -InterfaceIndex $_.ifIndex -ErrorAction SilentlyContinue;`+
		`   if ($a -and $a.Status -eq 'Up') { '{0}|{1}|{2}|{3}' -f ($_.RouteMetric + $m), $_.ifIndex, $_.NextHop, $a.InterfaceDescription } } |`+
		` Sort-Object { [int]($_.Split('|')[0]) }`, tunAlias)
	out, err := ps(script)
	if err != nil {
		return nil, fmt.Errorf("Get-NetRoute: %v: %s", err, out)
	}
	for _, line := range strings.Split(out, "\n") {
		p := strings.SplitN(strings.TrimSpace(line), "|", 4)
		if len(p) < 4 || virtualAdapter.MatchString(p[3]) {
			continue
		}
		return &physRoute{IfIndex: p[1], NextHop: p[2], Desc: p[3]}, nil
	}
	return nil, errors.New("no usable default route (network is down?)")
}

// rebindHost moves the host route of the Hysteria2 server to a new gateway
// after the physical network changed.
func (n *netConfig) rebindHost(rt *physRoute) error {
	if n.serverIP.To4() == nil || rt == nil || rt.NextHop == "" || rt.NextHop == "0.0.0.0" {
		n.hostGW = rt
		return nil
	}
	if n.hostGW != nil && n.hostGW.NextHop != "" && n.hostGW.NextHop != "0.0.0.0" {
		run("route", n.hostRouteArgs("delete")...)
	}
	n.hostGW = rt
	run("route", n.hostRouteArgs("delete")...) // stale leftovers, ignore errors
	if out, err := run("route", n.hostRouteArgs("add")...); err != nil {
		return fmt.Errorf("route add server: %v: %s", err, out)
	}
	return nil
}
