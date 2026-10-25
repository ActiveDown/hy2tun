package main

import (
	"encoding/json"
	"net/netip"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Domains that count as "RU": the national TLDs plus well-known services that
// live under foreign TLDs (Yandex CDN/API, VK CDN, ...).
var ruSuffixes = []string{".ru", ".su", ".xn--p1ai", "." + "рф"}

var ruDomains = []string{
	"yandex.net", "yandex.com", "yandex.by", "yandex.kz", "yandex.uz", "yastatic.net",
	"yandexcloud.net", "ya.cc", "yadi.sk", "kinopoisk.dev",
	"vk.com", "vk.me", "vk-cdn.net", "vkuser.net", "vkuseraudio.net", "userapi.com", "mycdn.me", "vkforms.ru",
	"2gis.com", "sberbank.com", "avito.st", "mail.ru",
}

func isRUName(name string) bool {
	name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
	if name == "" {
		return false
	}
	for _, s := range ruSuffixes {
		if strings.HasSuffix(name, s) || name == s[1:] {
			return true
		}
	}
	for _, d := range ruDomains {
		if name == d || strings.HasSuffix(name, "."+d) {
			return true
		}
	}
	return false
}

type via int

const (
	viaTunnel via = iota
	viaDirect
)

func (v via) String() string {
	if v == viaDirect {
		return "direct"
	}
	return "tunnel"
}

// dnsMap remembers which domain an IP was resolved from (learned from DNS
// answers that pass through the tunnel and from TLS SNI / HTTP Host).
type dnsMap struct {
	mu sync.RWMutex
	m  map[netip.Addr]dnsEntry
}

type dnsEntry struct {
	name string
	exp  time.Time
}

func newDNSMap() *dnsMap {
	d := &dnsMap{m: make(map[netip.Addr]dnsEntry)}
	go func() {
		for range time.Tick(time.Minute) {
			now := time.Now()
			d.mu.Lock()
			for k, v := range d.m {
				if now.After(v.exp) {
					delete(d.m, k)
				}
			}
			d.mu.Unlock()
		}
	}()
	return d
}

func (d *dnsMap) put(ip netip.Addr, name string, ttl time.Duration) {
	if ttl < 30*time.Second {
		ttl = 30 * time.Second
	}
	if ttl > 10*time.Minute {
		ttl = 10 * time.Minute
	}
	d.mu.Lock()
	d.m[ip.Unmap()] = dnsEntry{name: name, exp: time.Now().Add(ttl)}
	d.mu.Unlock()
}

func (d *dnsMap) get(ip netip.Addr) (string, bool) {
	d.mu.RLock()
	e, ok := d.m[ip.Unmap()]
	d.mu.RUnlock()
	if !ok || time.Now().After(e.exp) {
		return "", false
	}
	return e.name, true
}

type decisionLog struct {
	Time string `json:"time"`
	Proc string `json:"proc"`
	Name string `json:"name"`
	Dst  string `json:"dst"`
	Via  string `json:"via"`
	Why  string `json:"why"`
}

// router decides, per flow, whether it goes through Hysteria2 or directly.
type router struct {
	cfg       atomic.Pointer[compiledConfig]
	hasDirect bool // direct path available (physical interface known)
	physV6    bool // the physical adapter has working IPv6
	dns       *dnsMap
	geo       *geoRU
	settings  string
	onChange  func() // called after a configuration change

	directFlow atomic.Int64
	tunnelFlow atomic.Int64

	logMu sync.Mutex
	last  []decisionLog
}

func newRouter(settingsPath string, hasDirect bool) *router {
	r := &router{hasDirect: hasDirect, dns: newDNSMap(), geo: parseGeoRU(ruCIDRData), settings: settingsPath}
	cc, _ := compileConfig(defaultConfig())
	r.cfg.Store(cc)
	return r
}

// config returns the active configuration.
func (r *router) config() Config { return r.cfg.Load().Config }

func (r *router) proxyRU() bool { return r.cfg.Load().ProxyRU }

// load reads the persisted configuration; it reports whether a file existed.
// Older files that only contain {"proxy_ru":...} keep working.
func (r *router) load() bool {
	b, err := os.ReadFile(r.settings)
	if err != nil {
		return false
	}
	c := defaultConfig()
	if json.Unmarshal(b, &c) != nil {
		return false
	}
	cc, err := compileConfig(c)
	if err != nil {
		return false
	}
	r.cfg.Store(cc)
	return true
}

// setConfig validates, activates and persists a new configuration.
func (r *router) setConfig(c Config) (Config, error) {
	cc, err := compileConfig(c)
	if err != nil {
		return Config{}, err
	}
	r.cfg.Store(cc)
	b, _ := json.MarshalIndent(cc.Config, "", "  ")
	os.WriteFile(r.settings, b, 0o600)
	if r.onChange != nil {
		go r.onChange()
	}
	return cc.Config, nil
}

func (r *router) setProxyRU(on bool) {
	c := r.config()
	c.ProxyRU = on
	r.setConfig(c)
}

// decide picks the path for a flow. p is the owning process (nil if unknown),
// name the best known domain (may be empty), ip the destination.
func (r *router) decide(p *procInfo, name string, ip netip.Addr) (via, string) {
	c := r.cfg.Load()
	if !r.hasDirect {
		return viaTunnel, "no direct path"
	}
	if c.tunnel.match(name, ip) {
		return viaTunnel, "tunnel rule"
	}
	if c.VPNClientsDirect && isVPNClient(p) {
		return viaDirect, "VPN client"
	}
	if c.direct.match(name, ip) {
		return viaDirect, "direct rule"
	}
	if c.directAppMatches(p) {
		return viaDirect, "direct app"
	}
	base, why := viaTunnel, "all traffic"
	switch c.Mode {
	case modeSelected:
		switch {
		case p == nil:
			base, why = viaTunnel, "unknown app"
		case c.appMatches(p):
			base, why = viaTunnel, "selected app"
		default:
			base, why = viaDirect, "app not selected"
		}
	case modeExcept:
		switch {
		case p == nil:
			base, why = viaTunnel, "unknown app"
		case c.appMatches(p):
			base, why = viaDirect, "excluded app"
		default:
			base, why = viaTunnel, "app not excluded"
		}
	}
	if base == viaTunnel && !c.ProxyRU {
		if name != "" {
			if isRUName(name) {
				return viaDirect, "RU domain"
			}
		} else if c.GeoIPRU && ip.IsValid() && r.geo.contains(ip) {
			return viaDirect, "RU address"
		}
	}
	return base, why
}

// decideDNS chooses the resolver path for a query (the asking app is not
// known for DNS, so only name rules apply).
func (r *router) decideDNS(name string) via {
	c := r.cfg.Load()
	if !r.hasDirect {
		return viaTunnel
	}
	if c.tunnel.match(name, netip.Addr{}) {
		return viaTunnel
	}
	if c.direct.match(name, netip.Addr{}) {
		return viaDirect
	}
	if !c.ProxyRU && isRUName(name) {
		return viaDirect
	}
	return viaTunnel
}

func (r *router) record(proc, name, dst string, v via, why string) {
	if v == viaDirect {
		r.directFlow.Add(1)
	} else {
		r.tunnelFlow.Add(1)
	}
	r.logMu.Lock()
	r.last = append(r.last, decisionLog{Time: time.Now().Format("15:04:05"), Proc: proc, Name: name, Dst: dst, Via: v.String(), Why: why})
	if len(r.last) > 80 {
		r.last = r.last[len(r.last)-80:]
	}
	r.logMu.Unlock()
}

func (r *router) recent() []decisionLog {
	r.logMu.Lock()
	defer r.logMu.Unlock()
	out := make([]decisionLog, len(r.last))
	copy(out, r.last)
	return out
}
