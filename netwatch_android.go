package main

import (
	"net"
	"regexp"
)

// virtualAdapter never matches: Android runs a single VPN at a time.
var virtualAdapter = regexp.MustCompile(`^$x`)

func bestInterfaceFor(ip net.IP) (uint32, error) { return parseIfIndex(currentRoute().IfIndex) }

func physicalDefaultRoute(tunAlias string) (*physRoute, error) { return currentRoute(), nil }

func (n *netConfig) rebindHost(rt *physRoute) error {
	n.hostGW = rt
	return nil
}
