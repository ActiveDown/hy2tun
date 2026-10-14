package main

import (
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The test process opens real sockets and checks that the Windows connection
// tables resolve them back to this very process (and its parent chain).
func TestProcessLookupTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	src := uint16(c.LocalAddr().(*net.TCPAddr).Port)
	dst := ln.Addr().(*net.TCPAddr)
	pt := newProcTable()
	p := pt.lookupTCP(src, netip.MustParseAddr("127.0.0.1"), uint16(dst.Port))
	if p == nil {
		t.Fatal("owner of a live TCP connection not found")
	}
	if p.PID != uint32(os.Getpid()) {
		t.Fatalf("pid = %d, want %d", p.PID, os.Getpid())
	}
	exe, _ := os.Executable()
	want := strings.ToLower(filepath.Base(exe))
	if p.name() != want {
		t.Fatalf("name = %q, want %q", p.name(), want)
	}
	if len(p.Chain) < 2 {
		t.Fatalf("parent chain missing: %+v", p.Chain)
	}
	t.Logf("resolved: pid=%d name=%s parent=%s", p.PID, p.name(), p.Chain[1].Name)

	// and the app list in the panel sees us
	found := false
	for _, r := range pt.runningWithNetwork() {
		if r.Name == want {
			found = true
		}
	}
	if !found {
		t.Fatal("running process list does not contain the test process")
	}
}

// Regression: IPv6 rows used to be parsed with wrong offsets, so every IPv6
// connection showed up as an unknown process.
func TestProcessLookupTCPv6(t *testing.T) {
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skip("IPv6 loopback not available")
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	src := uint16(c.LocalAddr().(*net.TCPAddr).Port)
	dst := ln.Addr().(*net.TCPAddr)
	p := newProcTable().lookupTCP(src, netip.MustParseAddr("::1"), uint16(dst.Port))
	if p == nil || p.PID != uint32(os.Getpid()) {
		t.Fatalf("IPv6 owner not resolved: %+v", p)
	}
}

func TestProcessLookupUDP(t *testing.T) {
	u, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	port := uint16(u.LocalAddr().(*net.UDPAddr).Port)
	p := newProcTable().lookupUDP(port, false)
	if p == nil || p.PID != uint32(os.Getpid()) {
		t.Fatalf("udp owner: %+v", p)
	}
}

func TestProcessLookupMiss(t *testing.T) {
	if p := newProcTable().lookupTCP(1, netip.MustParseAddr("203.0.113.9"), 9); p != nil {
		t.Fatalf("unexpected owner for a non-existent connection: %+v", p)
	}
}

func TestAppPathMatch(t *testing.T) {
	cc, err := compileConfig(Config{Mode: modeSelected, Apps: []string{`C:\Program Files\Google\Chrome\Application\chrome.exe`}, IncludeChildren: true})
	if err != nil {
		t.Fatal(err)
	}
	p := &procInfo{Chain: []procRef{{Name: "chrome.exe", Path: `c:\program files\google\chrome\application\chrome.exe`}}}
	if !cc.appMatches(p) {
		t.Fatal("full path rule must match (case-insensitive)")
	}
	other := &procInfo{Chain: []procRef{{Name: "chrome.exe", Path: `d:\portable\chrome.exe`}}}
	if cc.appMatches(other) {
		t.Fatal("a different chrome.exe must not match a full-path rule")
	}
}
