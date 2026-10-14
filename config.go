package main

import (
	"fmt"
	"net/netip"
	"path/filepath"
	"strings"
)

// Modes of the routing policy.
const (
	modeAll      = "all"      // everything goes through the tunnel
	modeSelected = "selected" // only the listed apps go through the tunnel
	modeExcept   = "except"   // everything except the listed apps goes through
)

// Config is the user-visible routing policy (persisted in settings.json).
//
// Evaluation order for every flow:
//  1. TunnelDomains match  -> tunnel
//  2. DirectDomains match  -> direct
//  3. mode + Apps          -> tunnel or direct
//  4. if that gave "tunnel" and ProxyRU is off: .ru/.rf names (and, when the
//     name is unknown, RU IP addresses) -> direct
type Config struct {
	Mode            string   `json:"mode"`
	Apps            []string `json:"apps"`
	IncludeChildren bool     `json:"include_children"`
	DirectDomains   []string `json:"direct_domains"`
	TunnelDomains   []string `json:"tunnel_domains"`
	ProxyRU         bool     `json:"proxy_ru"`
	GeoIPRU         bool     `json:"geoip_ru"`
	// VPNClientsDirect sends the own traffic of other VPN clients (WireGuard,
	// OpenVPN, ...) straight out through the real network: their packets to the
	// VPN server must not be tunnelled into a second tunnel.
	VPNClientsDirect bool `json:"vpn_clients_direct"`
	// BlockDNSLeak drops DNS that would leave through a physical adapter while the
	// tunnel is up, so name resolution can only go through the tunnel.
	BlockDNSLeak bool `json:"block_dns_leak"`
	// KillSwitch (mode "selected" only) blocks the selected applications on the
	// physical adapters, so they cannot leak when the tunnel is down.
	KillSwitch bool `json:"kill_switch"`
}

func defaultConfig() Config {
	return Config{Mode: modeAll, IncludeChildren: true, GeoIPRU: true, VPNClientsDirect: true, BlockDNSLeak: true}
}

// vpnClients are executables whose network traffic is VPN transport.
var vpnClients = map[string]struct{}{
	"wireguard.exe": {}, "wg.exe": {}, "openvpn.exe": {}, "openvpnserv.exe": {}, "openvpnserv2.exe": {},
	"tailscaled.exe": {}, "zerotier-one_x64.exe": {}, "nebula.exe": {}, "softethervpnclient.exe": {},
	"vpnclient.exe": {}, "outline-service.exe": {}, "amneziawg.exe": {}, "amneziavpn-service.exe": {},
}

func isVPNClient(p *procInfo) bool {
	if p == nil || len(p.Chain) == 0 {
		return false
	}
	_, ok := vpnClients[p.Chain[0].Name]
	return ok
}

const (
	maxListEntries = 500
	maxEntryLen    = 260
)

func cleanList(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || len(s) > maxEntryLen {
			continue
		}
		k := strings.ToLower(s)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, s)
		if len(out) >= maxListEntries {
			break
		}
	}
	return out
}

// normalize validates c and returns a cleaned copy.
func (c Config) normalize() (Config, error) {
	switch c.Mode {
	case "":
		c.Mode = modeAll
	case modeAll, modeSelected, modeExcept:
	default:
		return c, fmt.Errorf("unknown mode %q", c.Mode)
	}
	c.Apps = cleanList(c.Apps)
	c.DirectDomains = cleanList(c.DirectDomains)
	c.TunnelDomains = cleanList(c.TunnelDomains)
	return c, nil
}

// ruleSet matches domain names (suffix match on label boundary) and IPs/CIDRs.
type ruleSet struct {
	suffixes []string
	prefixes []netip.Prefix
}

func compileRules(entries []string) ruleSet {
	var rs ruleSet
	for _, e := range entries {
		e = strings.ToLower(strings.TrimSpace(e))
		if p, err := netip.ParsePrefix(e); err == nil {
			rs.prefixes = append(rs.prefixes, p.Masked())
			continue
		}
		if a, err := netip.ParseAddr(e); err == nil {
			rs.prefixes = append(rs.prefixes, netip.PrefixFrom(a, a.BitLen()))
			continue
		}
		e = strings.TrimPrefix(e, "*.")
		e = strings.Trim(e, ".")
		if e != "" {
			rs.suffixes = append(rs.suffixes, e)
		}
	}
	return rs
}

func (rs *ruleSet) match(name string, ip netip.Addr) bool {
	if name != "" {
		name = strings.ToLower(strings.TrimSuffix(name, "."))
		for _, s := range rs.suffixes {
			if name == s || strings.HasSuffix(name, "."+s) {
				return true
			}
		}
	}
	if ip.IsValid() {
		ip = ip.Unmap()
		for _, p := range rs.prefixes {
			if p.Contains(ip) {
				return true
			}
		}
	}
	return false
}

// compiledConfig is the immutable, ready-to-evaluate form of Config.
type compiledConfig struct {
	Config
	appNames map[string]struct{}
	appPaths map[string]struct{}
	direct   ruleSet
	tunnel   ruleSet
}

func compileConfig(c Config) (*compiledConfig, error) {
	c, err := c.normalize()
	if err != nil {
		return nil, err
	}
	cc := &compiledConfig{
		Config:   c,
		appNames: map[string]struct{}{},
		appPaths: map[string]struct{}{},
		direct:   compileRules(c.DirectDomains),
		tunnel:   compileRules(c.TunnelDomains),
	}
	for _, a := range c.Apps {
		a = strings.ToLower(strings.Trim(a, `" `))
		if strings.ContainsAny(a, `\/`) {
			cc.appPaths[filepath.Clean(a)] = struct{}{}
		} else {
			cc.appNames[a] = struct{}{}
		}
	}
	return cc, nil
}

// appMatches reports whether the process (or, with IncludeChildren, one of its
// ancestors) is in the app list.
func (c *compiledConfig) appMatches(p *procInfo) bool {
	if p == nil {
		return false
	}
	chain := p.Chain
	if !c.IncludeChildren && len(chain) > 1 {
		chain = chain[:1]
	}
	for _, r := range chain {
		if _, ok := c.appNames[r.Name]; ok {
			return true
		}
		if r.Path != "" {
			if _, ok := c.appPaths[filepath.Clean(r.Path)]; ok {
				return true
			}
		}
	}
	return false
}
