package main

import (
	"net/netip"
	"os/exec"
	"strings"
)

// systemDNSServers returns the resolvers the primary network service uses (the
// router's, usually), read before the TUN resolver is published. They know names
// that public DNS does not: the ones behind a router-level VPN or a corporate
// network. skip lists addresses to leave out (the tunnel's own resolvers).
func systemDNSServers(skip []string) []string {
	cmd := exec.Command("scutil")
	cmd.Stdin = strings.NewReader("show State:/Network/Global/DNS\n")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	return parseScutilDNS(string(out), skip)
}

// parseScutilDNS pulls the ServerAddresses array out of a scutil dictionary dump.
func parseScutilDNS(dump string, skip []string) []string {
	var res []string
	in := false
	for _, ln := range strings.Split(dump, "\n") {
		ln = strings.TrimSpace(ln)
		switch {
		case strings.HasPrefix(ln, "ServerAddresses"):
			in = true
		case in && ln == "}":
			in = false
		case in:
			i := strings.Index(ln, ":")
			if i < 0 {
				continue
			}
			ip, err := netip.ParseAddr(strings.TrimSpace(ln[i+1:]))
			if err != nil || !ip.Is4() || ip.IsLoopback() || ip.IsUnspecified() {
				continue
			}
			s := ip.String()
			dup := false
			for _, k := range append(skip, res...) {
				if k == s {
					dup = true
				}
			}
			if !dup {
				res = append(res, s)
			}
		}
	}
	return res
}
