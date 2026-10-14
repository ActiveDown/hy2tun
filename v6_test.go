package main

import (
	"net/netip"
	"os"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"gvisor.dev/gvisor/pkg/tcpip"
)

func buildQuery(name string, t dnsmessage.Type) []byte {
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 0xBEEF, RecursionDesired: true})
	b.StartQuestions()
	b.Question(dnsmessage.Question{Name: dnsmessage.MustNewName(name), Type: t, Class: dnsmessage.ClassINET})
	out, _ := b.Finish()
	return out
}

func TestEmptyAAAAResponse(t *testing.T) {
	q := buildQuery("yandex.ru.", dnsmessage.TypeAAAA)
	n, qt := dnsQuestion(q)
	if n != "yandex.ru." || qt != dnsmessage.TypeAAAA {
		t.Fatalf("question parse: %q %v", n, qt)
	}
	resp := emptyDNSResponse(q)
	var m dnsmessage.Message
	if err := m.Unpack(resp); err != nil {
		t.Fatal(err)
	}
	if m.ID != 0xBEEF || !m.Response || m.RCode != dnsmessage.RCodeSuccess || len(m.Answers) != 0 || len(m.Questions) != 1 {
		t.Fatalf("bad NODATA response: %+v", m.Header)
	}
}

func TestDirectUnavailableV6(t *testing.T) {
	tn := &tunnel{rt: newRouter(os.DevNull, true)} // physV6 == false
	ip6 := netip.MustParseAddr("2a02:6b8:a::a")
	ip4 := netip.MustParseAddr("77.88.55.88")
	tn.rt.dns.put(ip6, "yandex.ru.", time.Minute)
	tn.rt.dns.put(ip4, "yandex.ru.", time.Minute)
	if !tn.directUnavailable(nil, ip6) {
		t.Fatal("v6 RU destination must be refused when the physical net has no IPv6")
	}
	if tn.directUnavailable(nil, ip4) {
		t.Fatal("v4 destination must not be refused")
	}
	c := tn.rt.config()
	c.ProxyRU = true
	tn.rt.setConfig(c) // goes through the tunnel -> IPv6 works there
	if tn.directUnavailable(nil, ip6) {
		t.Fatal("with proxy-ru on, v6 goes through the tunnel and must not be refused")
	}
	c.ProxyRU = false
	tn.rt.setConfig(c)
	tn.rt.physV6 = true
	if tn.directUnavailable(nil, ip6) {
		t.Fatal("with physical IPv6 nothing should be refused")
	}
}

func TestTunSubnetIsNonUnicast(t *testing.T) {
	if !isNonUnicast(tcpip.AddrFrom4([4]byte{198, 18, 77, 3})) {
		t.Fatal("TUN subnet broadcast must be dropped")
	}
	if isNonUnicast(tcpip.AddrFrom4([4]byte{8, 8, 8, 8})) {
		t.Fatal("8.8.8.8 is unicast")
	}
}
