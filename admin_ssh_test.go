package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// Exercise the actual SSH handshake, stdin delivery, stdout marker parser,
// connection cache and manager operations, without requiring a remote server.
type adminSSHFixture struct {
	mu          sync.Mutex
	config      string
	statsDown   bool
	service     string
	connections atomic.Int32
	commands    atomic.Int32
	address     string
	signer      ssh.Signer
}

func newAdminSSHFixture(t *testing.T) *adminSSHFixture {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	f := &adminSSHFixture{config: sampleAdminConfig, service: "active", address: ln.Addr().String(), signer: signer}
	cfg := &ssh.ServerConfig{NoClientAuth: true}
	cfg.AddHostKey(signer)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				cc, channels, requests, err := ssh.NewServerConn(conn, cfg)
				if err != nil {
					conn.Close()
					return
				}
				defer cc.Close()
				f.connections.Add(1)
				go func() {
					for req := range requests {
						req.Reply(req.Type == "keepalive@openssh.com", nil)
					}
				}()
				for ch := range channels {
					if ch.ChannelType() != "session" {
						ch.Reject(ssh.UnknownChannelType, "unsupported")
						continue
					}
					channel, requests, err := ch.Accept()
					if err != nil {
						continue
					}
					go func() {
						defer channel.Close()
						for req := range requests {
							if req.Type != "exec" {
								req.Reply(false, nil)
								continue
							}
							req.Reply(true, nil)
							script, _ := io.ReadAll(channel)
							out, exit := f.run(string(script))
							io.WriteString(channel, out)
							channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{exit}))
							return
						}
					}()
				}
			}()
		}
	}()
	return f
}

func (f *adminSSHFixture) run(script string) (string, uint32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands.Add(1)
	switch {
	case script == adminScriptReadConfig():
		return "HY2TUN_PAYLOAD\n" + f.config, 0
	case script == adminScriptStatus():
		return "HY2TUN_PAYLOAD\nservice=" + f.service + "\nconfig=config_ok\nversion=v2.test\nHY2TUN_CONFIG\n" + f.config, 0
	case strings.Contains(script, "HY2TUN_CONFIG\n"):
		_, tail, _ := strings.Cut(script, "<<'HY2TUN_CONFIG'\n")
		data, _, _ := strings.Cut(tail, "\nHY2TUN_CONFIG")
		decoded, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			return "HY2TUN_ERROR:config_invalid\n", 1
		}
		f.config = string(decoded)
		return "HY2TUN_PAYLOAD\nok\n", 0
	case strings.Contains(script, "curl -fsS"):
		if f.statsDown {
			return "HY2TUN_ERROR:stats_unavailable\n", 1
		}
		if strings.Contains(script, "/online\"") {
			return "HY2TUN_PAYLOAD\n{\"Admin\":2}\n", 0
		}
		return "HY2TUN_PAYLOAD\n{\"Admin\":{\"tx\":100,\"rx\":50}}\n", 0
	default:
		return "HY2TUN_ERROR:unexpected_script\n", 1
	}
}

func (f *adminSSHFixture) manager(t *testing.T) (*adminManager, adminConnectRequest) {
	t.Helper()
	e := newEngine(defaultEngineOpts(), t.TempDir(), false, newRingLog(10))
	srv, err := e.store.add("hy2://Admin:owner@127.0.0.1:443?sni=example.com&insecure=1#Test", "")
	if err != nil {
		t.Fatal(err)
	}
	m := newAdminManager(e)
	m.dial = func(ctx context.Context, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", f.address)
	}
	t.Cleanup(m.close)
	return m, adminConnectRequest{ID: srv.ID, Host: "127.0.0.1", Port: 22, Username: "root", Password: "test-password"}
}

func attachAdminFixture(t *testing.T, m *adminManager, req adminConnectRequest) {
	t.Helper()
	_, err := m.Attach(req)
	if err == nil {
		t.Fatal("unknown SSH key was trusted silently")
	}
	fp, ok := adminTrustNeed(err)
	if !ok {
		t.Fatal(err)
	}
	req.Trust, req.Fingerprint = true, fp
	st, err := m.Attach(req)
	if err != nil || !st.Attached || !st.SSH {
		t.Fatalf("first attach: %+v, %v", st, err)
	}
}

func TestAdminSSHAttachPollAndManageUsers(t *testing.T) {
	f := newAdminSSHFixture(t)
	m, req := f.manager(t)
	attachAdminFixture(t, m, req) // regression: drop(nil) used to panic here
	for i := 0; i < 3; i++ {
		st := m.Status(req.ID)
		if st.Error != "" || !st.SSH || st.Service != "active" || st.Config != adminConfigOk || st.Auth != "userpass" || !st.Stats {
			t.Fatalf("status: %+v", st)
		}
	}
	if f.connections.Load() != 1 {
		t.Fatal("polls must reuse the verified SSH connection")
	}
	// An expired previous deadline must not poison a cached connection.
	m.cur.conn.SetDeadline(time.Now().Add(-time.Second))
	if _, _, err := m.exec(m.cur, "root", adminScriptReadConfig()); err != nil {
		t.Fatalf("command deadline was not renewed: %v", err)
	}
	res, err := m.Users(req.ID)
	if err != nil || len(res.Users) != 1 || !res.Stats || res.Users[0].Online != 2 || res.Users[0].Tx != 100 {
		t.Fatalf("users: %+v %v", res, err)
	}
	if err := m.AddUser(req.ID, "tester", "p@ss:word"); err != nil {
		t.Fatal(err)
	}
	_, link, err := m.UserLink(req.ID, "tester")
	if err != nil {
		t.Fatal(err)
	}
	l, err := parseLink(link)
	if err != nil || l.Auth != "tester:p@ss:word" || l.SNI != "example.com" || !l.Insecure {
		t.Fatal("user link did not preserve credentials or TLS settings")
	}
	if err := m.DeleteUser(req.ID, "tester"); err != nil {
		t.Fatal(err)
	}
	if err := m.ResetStats(req.ID); err != nil {
		t.Fatal(err)
	}
	before := f.commands.Load()
	if m.AddUser(req.ID, "Admin", "replacement") == nil || m.DeleteUser(req.ID, "Admin") == nil || f.commands.Load() != before {
		t.Fatal("the owner must be protected before any SSH operation")
	}
	if err := m.Detach(req.ID); err != nil || m.cur != nil || m.e.admin.has(req.ID) {
		t.Fatalf("detach did not release access/connection: %v", err)
	}
}

func TestAdminSSHStatsOptionalAndFailureIsNotServiceFailure(t *testing.T) {
	f := newAdminSSHFixture(t)
	f.config = "listen: :443\nauth:\n  type: userpass\n  userpass:\n    Admin: owner\n"
	m, req := f.manager(t)
	attachAdminFixture(t, m, req)
	before := f.commands.Load()
	res, err := m.Users(req.ID)
	if err != nil || res.Stats || res.StatsError != "" || f.commands.Load()-before != 1 {
		t.Fatalf("disabled stats must not trigger probes: %+v %v", res, err)
	}
	if err := m.AddUser(req.ID, "no-stats", "password"); err != nil {
		t.Fatal(err)
	}
	if err := m.EnableStats(req.ID); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.statsDown = true
	f.mu.Unlock()
	res, err = m.Users(req.ID)
	if err != nil || res.Stats || res.StatsError == "" || len(res.Users) != 2 {
		t.Fatalf("stats failure must preserve the user list: %+v %v", res, err)
	}
	if st := m.Status(req.ID); st.Error != "" || st.Service != "active" {
		t.Fatalf("stats failure became a service error: %+v", st)
	}
}

func TestAdminSSHTrustAndCacheLifecycle(t *testing.T) {
	f := newAdminSSHFixture(t)
	m, req := f.manager(t)
	req.Trust, req.Fingerprint = true, "SHA256:not-the-displayed-key"
	if _, err := m.Attach(req); err == nil {
		t.Fatal("mismatching confirmation accepted")
	}
	req.Trust, req.Fingerprint = false, ""
	attachAdminFixture(t, m, req)
	first := m.cur
	m.idle = time.Now().Add(-adminIdleKeep - time.Second)
	if st := m.Status(req.ID); st.Error != "" || m.cur == first {
		t.Fatalf("expired cache was not replaced: %+v", st)
	}
	m.close()
	if err := os.Remove(filepath.Join(m.e.dir, "ssh-host-keys.json")); err != nil {
		t.Fatal(err)
	}
	if st := m.Status(req.ID); st.Error == "" || m.cur != nil {
		t.Fatalf("saved credentials must never auto-trust an unknown key: %+v", st)
	}
}

func TestAdminSSHHandshakeCancellation(t *testing.T) {
	m := &adminManager{}
	client, server := net.Pipe()
	defer server.Close()
	// Swallow the client banner but never send a server banner.
	go io.Copy(io.Discard, server)
	m.dial = func(context.Context, string) (net.Conn, error) { return client, nil }
	m.e = newEngine(defaultEngineOpts(), t.TempDir(), false, newRingLog(10))
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := m.connect(ctx, adminCredential{Host: "127.0.0.1", Port: 22}, ""); err == nil || time.Since(start) > time.Second {
		t.Fatal("SSH handshake ignored context cancellation")
	}
}

func TestAdminScriptSafety(t *testing.T) {
	status := adminScriptStatus()
	if !strings.Contains(status, "HY2TUN_PAYLOAD") || !strings.Contains(status, "is-active hysteria-server.service 2>/dev/null || true") {
		t.Fatal("status must emit payload and survive an inactive service under bash -e")
	}
	write := adminScriptWrite(base64.StdEncoding.EncodeToString([]byte("auth: {}\n")))
	if strings.Contains(write, "grep -qs '^trafficStats:'") || !strings.Contains(write, "rollback()") || !strings.Contains(write, `cp -p "$backup" /etc/hysteria/config.yaml`) {
		t.Fatal("writes must allow configs without stats and restore the previous config on restart failure")
	}
	for _, script := range []string{status, write, adminScriptProbe(9765, "secret'$(id)", "traffic")} {
		if !strings.HasPrefix(script, adminScriptHeader) {
			t.Fatal("script missing safe header")
		}
	}
}
