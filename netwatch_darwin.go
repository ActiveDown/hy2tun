//go:build darwin

package main

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
)

var virtualAdapter = regexp.MustCompile(`(?i)utun|wireguard|tun|tap|openvpn|nekoray|nekobox|sing-tun|tailscale|zerotier|hamachi|ppp|bridge|ipsec|lo[0-9]*`)

func bestInterfaceFor(ip net.IP) (uint32, error) {
	rt, err := bestRoute(ip)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseUint(rt.IfIndex, 10, 32)
	if err != nil {
		return 0, err
	}
	return uint32(n), nil
}

// physicalDefaultRoute finds the best physical default route that does not belong
// to our own TUN adapter or another virtual VPN adapter.
func physicalDefaultRoute(tunAlias string) (*physRoute, error) {
	out, err := run("netstat", "-rn", "-f", "inet")
	if err != nil {
		return nil, fmt.Errorf("netstat: %v: %s", err, out)
	}

	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 4 && fields[0] == "default" {
			gw := fields[1]
			netif := fields[3]
			if netif == tunAlias || virtualAdapter.MatchString(netif) {
				continue
			}
			iface, err := net.InterfaceByName(netif)
			if err != nil {
				continue
			}
			return &physRoute{
				IfIndex: strconv.Itoa(iface.Index),
				NextHop: gw,
				Desc:    netif,
			}, nil
		}
	}

	// Fallback to route -n get default if netstat parsing had no non-virtual matches
	if out, err := run("route", "-n", "get", "default"); err == nil {
		var gateway, ifName string
		for _, line := range strings.Split(out, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "gateway:") {
				gateway = strings.TrimSpace(strings.TrimPrefix(line, "gateway:"))
			} else if strings.HasPrefix(line, "interface:") {
				ifName = strings.TrimSpace(strings.TrimPrefix(line, "interface:"))
			}
		}
		if ifName != "" && ifName != tunAlias && !virtualAdapter.MatchString(ifName) {
			if iface, err := net.InterfaceByName(ifName); err == nil {
				return &physRoute{
					IfIndex: strconv.Itoa(iface.Index),
					NextHop: gateway,
					Desc:    ifName,
				}, nil
			}
		}
	}

	return nil, errors.New("no usable default route (network is down?)")
}

func (n *netConfig) rebindHost(rt *physRoute) error {
	if n.serverIP.To4() == nil || rt == nil {
		n.hostGW = rt
		n.addScopedDefault(rt)
		return nil
	}
	n.hostGW = rt
	n.addScopedDefault(rt)
	run("route", "delete", "-host", n.serverIP.String())
	var args []string
	if rt.NextHop != "" && rt.NextHop != "0.0.0.0" {
		args = []string{"add", "-host", n.serverIP.String(), rt.NextHop}
	} else if rt.Desc != "" {
		args = []string{"add", "-host", n.serverIP.String(), "-interface", rt.Desc}
	}
	if len(args) > 0 {
		if out, err := run("route", args...); err != nil {
			return fmt.Errorf("route add server: %v: %s", err, out)
		}
	}
	return nil
}
