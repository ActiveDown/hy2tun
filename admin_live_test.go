package main

import (
	"context"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// Opt-in, read-only smoke test. Credentials are supplied only through the
// environment; no server configuration or local credential store is written.
func TestAdminLiveReadOnly(t *testing.T) {
	host, password := os.Getenv("HY2TUN_TEST_SSH_HOST"), os.Getenv("HY2TUN_TEST_SSH_PASSWORD")
	if host == "" || password == "" {
		t.Skip("set HY2TUN_TEST_SSH_HOST and HY2TUN_TEST_SSH_PASSWORD for the read-only SSH smoke test")
	}
	e := newEngine(defaultEngineOpts(), t.TempDir(), false, newRingLog(10))
	m := newAdminManager(e)
	defer m.close()
	// Use the same physical-interface dialer as the application.
	c := adminCredential{Host: host, Port: 22, Username: "root", Password: password}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := m.connect(ctx, c, "")
	if err == nil {
		t.Fatal("a fresh host must require explicit key confirmation")
	}
	fp, ok := adminTrustNeed(err)
	if !ok {
		t.Fatal(err)
	}
	t.Logf("SSH host key: %s", fp)
	cc, err := m.connect(ctx, c, fp)
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()
	started := time.Now()
	payload, code, err := m.exec(cc, c.Username, adminScriptStatus())
	if err != nil || code != "" || !strings.Contains(payload, "service=active") {
		t.Fatalf("status: code=%q err=%v payload=%q", code, err, payload)
	}
	root, err := m.readConfig(cc, c)
	if err != nil {
		t.Fatal(err)
	}
	users, err := confUsers(root)
	if err != nil {
		t.Fatal(err)
	}
	if listen, secret, enabled := confTrafficStats(root); enabled {
		c.StatsPort, c.StatsSecret = adminListenPort(listen), secret
		for _, endpoint := range []string{"traffic", "online"} {
			if _, err := m.probe(cc, c, endpoint); err != nil {
				t.Fatalf("%s probe: %v", endpoint, err)
			}
		}
	}
	t.Logf("Read-only checks passed: %d users; elapsed %s; remote %s", len(users), time.Since(started).Round(time.Millisecond), net.JoinHostPort(host, "22"))
}
