package main

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/netip"
	"os"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func TestIsRUName(t *testing.T) {
	yes := []string{"yandex.ru", "WWW.Example.RU.", "ya.ru", "sub.site.su", "xn--80aswg.xn--p1ai", "пример.рф",
		"hdrc.yandex.net", "quic.egress.yandex.net", "vk.com", "cdn.userapi.com", "yastatic.net"}
	no := []string{"google.com", "example.org", "notyandex.net", "ru.example.com", "fakeru", "cloudflare.com", ""}
	for _, n := range yes {
		if !isRUName(n) {
			t.Errorf("%q should be RU", n)
		}
	}
	for _, n := range no {
		if isRUName(n) {
			t.Errorf("%q should not be RU", n)
		}
	}
}

func proc(names ...string) *procInfo {
	p := &procInfo{PID: 1}
	for _, n := range names {
		p.Chain = append(p.Chain, procRef{Name: n})
	}
	return p
}

var noIP = netip.Addr{}

func TestDecideAllMode(t *testing.T) {
	r := newRouter(os.DevNull, true)
	if v, _ := r.decide(nil, "yandex.ru", noIP); v != viaDirect {
		t.Fatal("yandex.ru should be direct when proxy-ru is off")
	}
	if v, _ := r.decide(nil, "google.com", noIP); v != viaTunnel {
		t.Fatal("google.com should use the tunnel")
	}
	c := r.config()
	c.ProxyRU = true
	r.setConfig(c)
	if v, _ := r.decide(nil, "yandex.ru", noIP); v != viaTunnel {
		t.Fatal("yandex.ru should use the tunnel when proxy-ru is on")
	}
	r2 := newRouter(os.DevNull, false)
	if v, _ := r2.decide(nil, "yandex.ru", noIP); v != viaTunnel {
		t.Fatal("without a direct path everything must use the tunnel")
	}
}

func TestSelectedMode(t *testing.T) {
	r := newRouter(os.DevNull, true)
	c := defaultConfig()
	c.Mode = modeSelected
	c.Apps = []string{"Chrome.exe"}
	r.setConfig(c)

	chrome := proc("chrome.exe", "explorer.exe")
	steam := proc("steam.exe", "explorer.exe")
	if v, _ := r.decide(chrome, "google.com", noIP); v != viaTunnel {
		t.Fatal("selected app must use the tunnel")
	}
	if v, _ := r.decide(steam, "google.com", noIP); v != viaDirect {
		t.Fatal("other apps must go direct in selected mode")
	}
	// exception inside the selected app: .ru goes direct
	if v, why := r.decide(chrome, "mail.ru", noIP); v != viaDirect || why != "RU domain" {
		t.Fatalf("RU exception for selected app: %v %q", v, why)
	}
	// unknown process fails closed (tunnel)
	if v, _ := r.decide(nil, "google.com", noIP); v != viaTunnel {
		t.Fatal("unknown process must fail closed to the tunnel")
	}
}

func TestExceptMode(t *testing.T) {
	r := newRouter(os.DevNull, true)
	c := defaultConfig()
	c.Mode = modeExcept
	c.Apps = []string{"steam.exe"}
	r.setConfig(c)
	if v, _ := r.decide(proc("steam.exe"), "example.com", noIP); v != viaDirect {
		t.Fatal("excluded app must go direct")
	}
	if v, _ := r.decide(proc("chrome.exe"), "example.com", noIP); v != viaTunnel {
		t.Fatal("other apps must use the tunnel")
	}
}

func TestChildProcesses(t *testing.T) {
	r := newRouter(os.DevNull, true)
	c := defaultConfig()
	c.Mode = modeSelected
	c.Apps = []string{"launcher.exe"}
	r.setConfig(c)
	game := proc("game.exe", "launcher.exe", "explorer.exe")
	if v, _ := r.decide(game, "example.com", noIP); v != viaTunnel {
		t.Fatal("child of a selected app must be selected too")
	}
	c.IncludeChildren = false
	r.setConfig(c)
	if v, _ := r.decide(game, "example.com", noIP); v != viaDirect {
		t.Fatal("with include_children off the child is not selected")
	}
}

func TestDomainRulesBeatApps(t *testing.T) {
	r := newRouter(os.DevNull, true)
	c := defaultConfig()
	c.Mode = modeSelected
	c.Apps = []string{"chrome.exe"}
	c.DirectDomains = []string{"bank.example.com", "10.0.0.0/8"}
	c.TunnelDomains = []string{"*.blocked.org"}
	r.setConfig(c)
	chrome, other := proc("chrome.exe"), proc("steam.exe")
	if v, why := r.decide(chrome, "login.bank.example.com", noIP); v != viaDirect || why != "direct rule" {
		t.Fatalf("direct exception: %v %q", v, why)
	}
	if v, _ := r.decide(chrome, "", netip.MustParseAddr("10.1.2.3")); v != viaDirect {
		t.Fatal("CIDR exception must apply")
	}
	if v, why := r.decide(other, "x.blocked.org", noIP); v != viaTunnel || why != "tunnel rule" {
		t.Fatalf("tunnel rule must beat 'app not selected': %v %q", v, why)
	}
	if v, _ := r.decide(chrome, "example.org", noIP); v != viaTunnel {
		t.Fatal("unrelated domain stays on the app's path")
	}
}

func TestGeoIPRU(t *testing.T) {
	r := newRouter(os.DevNull, true)
	if r.geo.size() < 5000 {
		t.Fatalf("RU list looks truncated: %d prefixes", r.geo.size())
	}
	ruIP := netip.MustParseAddr("77.88.8.8") // Yandex DNS
	usIP := netip.MustParseAddr("8.8.8.8")
	if !r.geo.contains(ruIP) || r.geo.contains(usIP) {
		t.Fatal("geo lookup is wrong")
	}
	// no name known: address decides
	if v, why := r.decide(nil, "", ruIP); v != viaDirect || why != "RU address" {
		t.Fatalf("unnamed RU address: %v %q", v, why)
	}
	if v, _ := r.decide(nil, "", usIP); v != viaTunnel {
		t.Fatal("unnamed foreign address must use the tunnel")
	}
	// a known foreign name wins over a RU-looking address (e.g. a CDN cache)
	if v, _ := r.decide(nil, "youtube.com", ruIP); v != viaTunnel {
		t.Fatal("known non-RU name must not be sent direct by address")
	}
	c := r.config()
	c.GeoIPRU = false
	r.setConfig(c)
	if v, _ := r.decide(nil, "", ruIP); v != viaTunnel {
		t.Fatal("geoip_ru=false must disable address rule")
	}
}

func TestSettingsBackwardCompat(t *testing.T) {
	f := t.TempDir() + "/settings.json"
	os.WriteFile(f, []byte(`{"proxy_ru":true}`), 0o600)
	r := newRouter(f, true)
	if !r.load() {
		t.Fatal("load failed")
	}
	c := r.config()
	if !c.ProxyRU || c.Mode != modeAll || !c.IncludeChildren || !c.GeoIPRU {
		t.Fatalf("old settings file must keep working with defaults: %+v", c)
	}
}

func TestConfigValidation(t *testing.T) {
	if _, err := (Config{Mode: "bogus"}).normalize(); err == nil {
		t.Fatal("unknown mode must be rejected")
	}
	c, err := (Config{Apps: []string{" a.exe ", "A.EXE", ""}}).normalize()
	if err != nil || len(c.Apps) != 1 || c.Mode != modeAll {
		t.Fatalf("normalize: %+v %v", c, err)
	}
}
func TestSNIFromRealClientHello(t *testing.T) {
	for _, host := range []string{"mail.ru", "www.google.com"} {
		c1, c2 := net.Pipe()
		go func() {
			tc := tls.Client(c1, &tls.Config{ServerName: host, InsecureSkipVerify: true})
			tc.Handshake()
		}()
		buf := make([]byte, 4096)
		c2.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, _ := c2.Read(buf)
		c1.Close()
		c2.Close()
		got, data := func() (string, []byte) { return sniFromClientHello(buf[:n]), buf[:n] }()
		if got != host {
			t.Errorf("SNI = %q, want %q (read %d bytes)", got, host, len(data))
		}
	}
}

func TestHostFromHTTP(t *testing.T) {
	req := []byte("GET / HTTP/1.1\r\nUser-Agent: x\r\nHost: Example.RU:8080\r\n\r\n")
	if !isHTTPMethod(req) {
		t.Fatal("not recognised as HTTP")
	}
	if got := hostFromHTTP(req); got != "Example.RU" {
		t.Fatalf("host = %q", got)
	}
}

func TestPeekName(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	go func() {
		c1.Write([]byte("GET / HTTP/1.1\r\nHost: yandex.ru\r\n\r\n"))
	}()
	name, data := peekName(c2, 500*time.Millisecond)
	if name != "yandex.ru" || len(data) == 0 {
		t.Fatalf("name=%q data=%d", name, len(data))
	}
}

func TestLearnDNS(t *testing.T) {
	q := dnsmessage.MustNewName("www.site.ru.")
	cname := dnsmessage.MustNewName("edge.cdn.net.")
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{Response: true})
	b.EnableCompression()
	b.StartQuestions()
	b.Question(dnsmessage.Question{Name: q, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET})
	b.StartAnswers()
	b.CNAMEResource(dnsmessage.ResourceHeader{Name: q, Class: dnsmessage.ClassINET, TTL: 60}, dnsmessage.CNAMEResource{CNAME: cname})
	b.AResource(dnsmessage.ResourceHeader{Name: cname, Class: dnsmessage.ClassINET, TTL: 60}, dnsmessage.AResource{A: [4]byte{203, 0, 113, 7}})
	msg, err := b.Finish()
	if err != nil {
		t.Fatal(err)
	}
	tn := &tunnel{rt: newRouter(os.DevNull, true)}
	tn.learnDNS(msg)
	name, ok := tn.rt.dns.get(netip.MustParseAddr("203.0.113.7"))
	if !ok || name != "www.site.ru." {
		t.Fatalf("learned %q ok=%v (the QUESTION name must win over the CNAME owner)", name, ok)
	}
	if v, _ := tn.rt.decide(nil, name, netip.Addr{}); v != viaDirect {
		t.Fatal("learned .ru name should route direct")
	}
}

// Uses the real physical interface: proves IP_UNICAST_IF accepts our setup.
// Set HY2_IFINDEX to the physical interface index to run it.
func TestDirectDial(t *testing.T) {
	idx := os.Getenv("HY2_IFINDEX")
	if idx == "" {
		t.Skip("HY2_IFINDEX not set")
	}
	dd, err := newDirectDialer(idx)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	c, err := dd.dialTCP(ctx, "1.1.1.1:80")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte("HEAD / HTTP/1.0\r\nHost: one.one.one.one\r\n\r\n"))
	buf := make([]byte, 16)
	n, _ := io.ReadFull(c, buf)
	if n < 8 || string(buf[:4]) != "HTTP" {
		t.Fatalf("unexpected reply %q", buf[:n])
	}
	u, err := dd.dialUDP(ctx, "77.88.8.8:53")
	if err != nil {
		t.Fatal(err)
	}
	u.Close()
}

func TestVPNClientsGoDirect(t *testing.T) {
	r := newRouter(os.DevNull, true)
	wg := proc("wireguard.exe", "services.exe")
	if v, why := r.decide(wg, "vpn.example.org", netip.MustParseAddr("198.51.100.7")); v != viaDirect || why != "VPN client" {
		t.Fatalf("WireGuard transport must go direct: %v %q", v, why)
	}
	// an explicit tunnel rule still wins
	c := r.config()
	c.TunnelDomains = []string{"download.wireguard.com"}
	r.setConfig(c)
	if v, _ := r.decide(wg, "download.wireguard.com", netip.Addr{}); v != viaTunnel {
		t.Fatal("explicit tunnel rule must beat the VPN-client rule")
	}
	c.VPNClientsDirect = false
	c.TunnelDomains = nil
	r.setConfig(c)
	if v, _ := r.decide(wg, "vpn.example.org", netip.MustParseAddr("198.51.100.7")); v != viaTunnel {
		t.Fatal("with the option off WireGuard follows the normal rule")
	}
}

func TestDirectAppsAndMacVPNClients(t *testing.T) {
	r := newRouter(t.TempDir()+"/settings.json", true)
	c := defaultConfig()
	c.DirectApps = []string{"Windows"}
	if _, err := r.setConfig(c); err != nil {
		t.Fatal(err)
	}
	if v, why := r.decide(proc("windows"), "rdp.example.com", noIP); v != viaDirect || why != "direct app" {
		t.Errorf("direct app: %v %q", v, why)
	}
	// a helper whose ancestor is the listed app (IncludeChildren is on by default)
	if v, _ := r.decide(proc("rdp helper", "windows"), "", netip.MustParseAddr("203.0.113.9")); v != viaDirect {
		t.Errorf("child of a direct app should go direct")
	}
	if v, _ := r.decide(proc("safari"), "example.org", noIP); v != viaTunnel {
		t.Errorf("unlisted app should use the tunnel")
	}
	for _, n := range []string{"wireguardnetworkextension", "openvpn connect", "ovpnagent", "amneziavpn-service", "v2box"} {
		if v, why := r.decide(proc(n), "vpn.example.net", noIP); v != viaDirect || why != "VPN client" {
			t.Errorf("%s: %v %q", n, v, why)
		}
	}
}
