package main

import (
	"path/filepath"
	"testing"
)

func TestPhysicalAdapterLUIDs(t *testing.T) {
	l := physicalAdapterLUIDs("Hy2Tun")
	// every machine that can run this test has at least one real adapter
	// (it may be down, that is fine)
	t.Logf("physical adapters: %d %v", len(l), l)
	if len(l) == 0 {
		t.Skip("no physical adapters on this machine")
	}
	for i := 1; i < len(l); i++ {
		if l[i] == l[i-1] {
			t.Fatal("duplicate LUID")
		}
	}
}

func TestDesiredFirewall(t *testing.T) {
	dir := t.TempDir()
	e := newEngine(defaultEngineOpts(), dir, false, nil)

	// idle + defaults: nothing to enforce (DNS block only while connected)
	if s := e.desiredFirewall(); s.BlockDNS || len(s.KillApps) != 0 || len(s.LUIDs) != 0 {
		t.Fatalf("idle spec must be empty: %+v", s)
	}

	// kill switch only in "selected" mode and only when enabled
	c := e.router.config()
	c.Mode = modeSelected
	c.Apps = []string{`C:\Program Files\Fake\app.exe`, "nosuchprocess-xyz.exe"}
	c.KillSwitch = false
	e.router.setConfig(c)
	if s := e.desiredFirewall(); len(s.KillApps) != 0 {
		t.Fatalf("kill switch off, got %v", s.KillApps)
	}
	c.KillSwitch = true
	e.router.setConfig(c)
	s := e.desiredFirewall()
	want := filepath.Clean(`c:\program files\fake\app.exe`)
	if len(s.KillApps) != 1 || s.KillApps[0] != want {
		t.Fatalf("kill apps = %v, want [%s]", s.KillApps, want)
	}
	if len(s.LUIDs) == 0 {
		t.Skip("no physical adapters")
	}
	// other modes never get a kill switch
	c.Mode = modeExcept
	e.router.setConfig(c)
	if s := e.desiredFirewall(); len(s.KillApps) != 0 {
		t.Fatalf("except mode must not have a kill switch: %v", s.KillApps)
	}
}

func TestAppPathCachePersists(t *testing.T) {
	dir := t.TempDir()
	c := loadAppPathCache(dir)
	if !c.add("chrome.exe", `c:\a\chrome.exe`) || c.add("chrome.exe", `c:\a\chrome.exe`) {
		t.Fatal("add/dedupe broken")
	}
	c2 := loadAppPathCache(dir)
	if got := c2.get("chrome.exe"); len(got) != 1 || got[0] != `c:\a\chrome.exe` {
		t.Fatalf("persisted: %v", got)
	}
}

func TestSpecSignatureStable(t *testing.T) {
	e := newEngine(defaultEngineOpts(), t.TempDir(), false, nil)
	a := specSignature(e.desiredFirewall())
	b := specSignature(e.desiredFirewall())
	if a != b {
		t.Fatalf("signature must be deterministic: %q vs %q", a, b)
	}
}
