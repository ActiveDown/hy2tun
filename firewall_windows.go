package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/ActiveDown/hy2tun/internal/wfp"
)

// Adapter types that carry real internet traffic.
const (
	ifTypeEthernet = 6
	ifTypeWiFi     = 71
	ifTypeWWAN1    = 243
	ifTypeWWAN2    = 244
)

var notPhysical = regexp.MustCompile(`(?i)virtual|vethernet|vmware|virtualbox|hyper-v|loopback|bluetooth|wintun|wireguard|tap-|openvpn|tailscale|zerotier|hamachi|nekoray|nekobox|check point`)

// physicalAdapterLUIDs lists the real network adapters (all of them, up or
// down, so rules are in place the moment an adapter comes up).
func physicalAdapterLUIDs(tunName string) []uint64 {
	size := uint32(16 << 10)
	var buf []byte
	var head *windows.IpAdapterAddresses
	for try := 0; try < 5; try++ {
		buf = make([]byte, size)
		head = (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC,
			windows.GAA_FLAG_SKIP_ANYCAST|windows.GAA_FLAG_SKIP_MULTICAST|windows.GAA_FLAG_SKIP_DNS_SERVER, 0, head, &size)
		if err == nil {
			break
		}
		if err != windows.ERROR_BUFFER_OVERFLOW {
			return nil
		}
		head = nil
	}
	var out []uint64
	for a := head; a != nil; a = a.Next {
		switch a.IfType {
		case ifTypeEthernet, ifTypeWiFi, ifTypeWWAN1, ifTypeWWAN2:
		default:
			continue
		}
		desc := windows.UTF16PtrToString(a.Description)
		name := windows.UTF16PtrToString(a.FriendlyName)
		if strings.EqualFold(name, tunName) || notPhysical.MatchString(desc) || notPhysical.MatchString(name) {
			continue
		}
		out = append(out, a.Luid)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// appPathCache remembers where each application name was seen running, so the
// kill switch still works while the application is not running right now.
type appPathCache struct {
	mu   sync.Mutex
	path string
	m    map[string][]string
}

func loadAppPathCache(dir string) *appPathCache {
	c := &appPathCache{path: filepath.Join(dir, "apppaths.json"), m: map[string][]string{}}
	if b, err := os.ReadFile(c.path); err == nil {
		json.Unmarshal(b, &c.m)
	}
	return c
}

func (c *appPathCache) add(name, path string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range c.m[name] {
		if p == path {
			return false
		}
	}
	c.m[name] = append(c.m[name], path)
	b, _ := json.MarshalIndent(c.m, "", "  ")
	os.WriteFile(c.path, b, 0o600)
	return true
}

func (c *appPathCache) get(name string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.m[name]...)
}

// killAppPaths resolves the selected applications to executable paths.
func (e *Engine) killAppPaths(cfg Config) []string {
	if cfg.Mode != modeSelected || !cfg.KillSwitch {
		return nil
	}
	names := map[string]struct{}{}
	set := map[string]struct{}{}
	for _, a := range cfg.Apps {
		a = strings.ToLower(strings.Trim(a, `" `))
		if strings.ContainsAny(a, `\/`) {
			set[filepath.Clean(a)] = struct{}{}
		} else if a != "" {
			names[a] = struct{}{}
		}
	}
	for name, paths := range e.procs.pathsByName(names) {
		for _, p := range paths {
			e.appPaths.add(name, p)
		}
	}
	for name := range names {
		for _, p := range e.appPaths.get(name) {
			set[p] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// firewallState throttles repeated failures.
type firewallState struct {
	mu      sync.Mutex
	sig     string
	failed  time.Time
	lastErr string
}

func specSignature(s wfp.Spec) string {
	return fmt.Sprintf("dns=%v luids=%v apps=%s", s.BlockDNS, s.LUIDs, strings.Join(s.KillApps, "|"))
}

// desiredFirewall computes what the WFP policy should look like right now.
func (e *Engine) desiredFirewall() wfp.Spec {
	cfg := e.router.config()
	e.mu.Lock()
	connected := e.cur != nil
	e.mu.Unlock()
	s := wfp.Spec{
		BlockDNS: connected && cfg.BlockDNSLeak,
		KillApps: e.killAppPaths(cfg),
	}
	if s.BlockDNS || len(s.KillApps) > 0 {
		s.LUIDs = physicalAdapterLUIDs(e.opts.TunName)
	}
	return s
}

// applyFirewall brings the WFP policy in line with the engine state. It is
// cheap when nothing changed and safe to call from anywhere.
func (e *Engine) applyFirewall() {
	if !isAdmin() {
		return
	}
	spec := e.desiredFirewall()
	sig := specSignature(spec)
	if !spec.BlockDNS && len(spec.KillApps) == 0 {
		sig = "off"
	}

	e.fw.mu.Lock()
	defer e.fw.mu.Unlock()
	if sig == e.fw.sig {
		return
	}
	if e.fw.lastErr != "" && sig == e.fw.lastErr && time.Since(e.fw.failed) < 15*time.Second {
		return
	}
	if err := wfp.Apply(spec); err != nil {
		e.fw.lastErr, e.fw.failed = sig, time.Now()
		log.Printf("firewall: %v", err)
		return
	}
	e.fw.sig, e.fw.lastErr = sig, ""
	if sig == "off" {
		log.Printf("firewall: rules removed")
	} else {
		log.Printf("firewall: DNS leak block=%v, kill switch for %d program(s), %d adapter(s)",
			spec.BlockDNS, len(spec.KillApps), len(spec.LUIDs))
	}
}

// firewallLoop re-checks the policy every few seconds (adapters appear and
// disappear, applications start for the first time).
func (e *Engine) firewallLoop() {
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-e.quit:
			return
		case <-t.C:
			e.applyFirewall()
		}
	}
}
