package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

type provisionSSHScript struct{ command, stdin string }

// An actual SSH handshake over loopback exercises host-key ordering, password
// authentication and streaming. It never executes the received install script.
type provisionSSHFixture struct {
	key    ssh.Signer
	addr   string
	auth   chan string
	script chan provisionSSHScript
	ln     net.Listener
	mu     sync.Mutex
	conns  []net.Conn
	output string
	exit   uint32
}

func newProvisionSSHFixture(t *testing.T, output string, exit uint32) *provisionSSHFixture {
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
	f := &provisionSSHFixture{key: signer, addr: ln.Addr().String(), ln: ln, auth: make(chan string, 4), script: make(chan provisionSSHScript, 4), output: output, exit: exit}
	t.Cleanup(func() {
		ln.Close()
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, c := range f.conns {
			c.Close()
		}
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.conns = append(f.conns, c)
			f.mu.Unlock()
			go f.serve(c)
		}
	}()
	return f
}

func (f *provisionSSHFixture) serve(c net.Conn) {
	defer c.Close()
	cfg := &ssh.ServerConfig{PasswordCallback: func(_ ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		f.auth <- string(password)
		return nil, nil
	}}
	cfg.AddHostKey(f.key)
	server, chans, requests, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	} // key rejection/cancellation is expected in tests
	defer server.Close()
	go ssh.DiscardRequests(requests)
	for ch := range chans {
		if ch.ChannelType() != "session" {
			ch.Reject(ssh.UnknownChannelType, "session only")
			continue
		}
		channel, requests, err := ch.Accept()
		if err != nil {
			return
		}
		for req := range requests {
			if req.Type != "exec" {
				req.Reply(false, nil)
				continue
			}
			var command struct{ Command string }
			if ssh.Unmarshal(req.Payload, &command) != nil {
				req.Reply(false, nil)
				continue
			}
			req.Reply(true, nil)
			input, _ := io.ReadAll(io.LimitReader(channel, 1<<20))
			f.script <- provisionSSHScript{command.Command, string(input)}
			io.WriteString(channel, f.output)
			channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{f.exit}))
			channel.Close()
			break
		}
	}
}

func newProvisionTestManager(t *testing.T, f *provisionSSHFixture) (*provisionManager, provisionRequest) {
	t.Helper()
	e := newEngine(defaultEngineOpts(), t.TempDir(), false, newRingLog(10))
	m := newProvisionManager(e)
	m.dial = func(ctx context.Context, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", f.addr)
	}
	m.verify = func(context.Context, *hy2Link, net.IP) error { return nil }
	t.Cleanup(m.close)
	_, port, _ := net.SplitHostPort(f.addr)
	n, _ := strconv.Atoi(port)
	return m, provisionRequest{Host: "127.0.0.1", SSHPort: n, Username: "root", Password: "ssh-secret-never-store", Domain: "vpn.example.com", Email: "owner@example.com", Name: "Мой сервер"}
}

func waitProvision(t *testing.T, m *provisionManager, accept func(provisionStatus) bool) provisionStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s := m.status(); accept(s) {
			return s
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("deployment did not reach expected state: %+v", m.status())
	return provisionStatus{}
}

func terminalProvision(t *testing.T, m *provisionManager) provisionStatus {
	return waitProvision(t, m, func(s provisionStatus) bool { return s.State == "failed" || s.State == "succeeded" })
}

func trustProvision(t *testing.T, m *provisionManager, key ssh.PublicKey) provisionStatus {
	t.Helper()
	s := waitProvision(t, m, func(s provisionStatus) bool { return s.State == "awaiting_host_key" })
	if s.Fingerprint != ssh.FingerprintSHA256(key) {
		t.Fatalf("wrong fingerprint: %s", s.Fingerprint)
	}
	if _, err := m.action(s.ID, true, false); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestProvisionTrustBeforePasswordAndSuccessfulSave(t *testing.T) {
	f := newProvisionSSHFixture(t, "installer-private-secret\nHY2TUN_STAGE:not-a-stage-secret\nHY2TUN_STAGE:install\nHY2TUN_CONFIGURED\nHY2TUN_READY\n", 0)
	m, req := newProvisionTestManager(t, f)
	verified := make(chan hy2Link, 1)
	m.verify = func(_ context.Context, l *hy2Link, ip net.IP) error {
		if !ip.Equal(net.ParseIP("127.0.0.1")) {
			return errors.New("wrong pinned IP")
		}
		verified <- *l
		return nil
	}
	if _, err := m.start(req); err != nil {
		t.Fatal(err)
	}
	awaiting := waitProvision(t, m, func(s provisionStatus) bool { return s.State == "awaiting_host_key" })
	select {
	case <-f.auth:
		t.Fatal("password was sent before host key approval")
	default:
	}
	if awaiting.Fingerprint != ssh.FingerprintSHA256(f.key.PublicKey()) {
		t.Fatal("wrong host fingerprint")
	}
	if _, err := m.action(awaiting.ID, true, false); err != nil {
		t.Fatal(err)
	}
	s := terminalProvision(t, m)
	if s.State != "succeeded" || s.Stage != "done" || s.ServerID == "" || s.Warning != "" {
		t.Fatalf("status: %+v", s)
	}
	select {
	case password := <-f.auth:
		if password != req.Password {
			t.Fatal("wrong SSH password")
		}
	default:
		t.Fatal("SSH authentication was not performed")
	}
	profile, ok := m.e.store.getExact(s.ServerID)
	if !ok || profile.Name != req.Name {
		t.Fatalf("profile missing: %+v", profile)
	}
	parsed, err := parseLink(profile.Link)
	if err != nil || parsed.SNI != req.Domain || parsed.Host != req.Domain || !strings.HasPrefix(parsed.Auth, "Admin:") || len(parsed.Auth) != len("Admin:")+48 {
		t.Fatalf("bad generated profile: %+v, %v", parsed, err)
	}
	select {
	case checked := <-verified:
		if checked.Auth != parsed.Auth {
			t.Fatal("verified a different profile")
		}
	default:
		t.Fatal("no VPN verification")
	}
	select {
	case script := <-f.script:
		if script.command != "bash -se" || !strings.Contains(script.stdin, "HY2TUN_READY") || strings.Contains(script.stdin, req.Password) {
			t.Fatal("invalid remote command or leaked SSH password")
		}
	default:
		t.Fatal("no remote script")
	}
	keys, err := m.readHostKeys()
	if err != nil || keys[net.JoinHostPort(req.Host, strconv.Itoa(req.SSHPort))] != string(ssh.MarshalAuthorizedKey(f.key.PublicKey())) {
		t.Fatalf("host key was not persisted: %v", err)
	}
	info, err := os.Stat(filepath.Join(m.e.dir, "ssh-host-keys.json"))
	if err != nil {
		t.Fatalf("host key file is missing: %v", err)
	}
	// Windows access control uses ACLs, not the POSIX permission bits exposed
	// by os.FileMode. The SSH persistence assertions above apply on every OS.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		t.Fatal("host key file is not private")
	}
	statusJSON, _ := json.Marshal(s)
	for _, secret := range []string{req.Password, parsed.Auth, "installer-private-secret", "not-a-stage-secret"} {
		if strings.Contains(string(statusJSON), secret) {
			t.Fatal("secret escaped through deployment status")
		}
	}
	stored := newServerStore(m.e.dir, false)
	if saved, ok := stored.getExact(s.ServerID); !ok || saved.Link != profile.Link {
		t.Fatal("profile was not persisted")
	}
}

func TestProvisionChangedHostKeyRejectsBeforePassword(t *testing.T) {
	f := newProvisionSSHFixture(t, "HY2TUN_READY\n", 0)
	m, req := newProvisionTestManager(t, f)
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	otherSigner, _ := ssh.NewSignerFromKey(other)
	if err := m.writeHostKeys(map[string]string{net.JoinHostPort(req.Host, strconv.Itoa(req.SSHPort)): string(ssh.MarshalAuthorizedKey(otherSigner.PublicKey()))}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.start(req); err != nil {
		t.Fatal(err)
	}
	s := terminalProvision(t, m)
	if s.State != "failed" || !strings.Contains(s.Error, "host key has changed") || m.e.store.count() != 0 {
		t.Fatalf("changed key accepted: %+v", s)
	}
	select {
	case <-f.auth:
		t.Fatal("password sent to changed host key")
	default:
	}
}

func TestProvisionFailureRetainsProfileOnlyAfterConfigured(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		profiles     int
	}{
		{"before configuration", "HY2TUN_STAGE:install\nHY2TUN_ERROR:install_failed\n", 0},
		{"after configuration", "HY2TUN_CONFIGURED\nHY2TUN_ERROR:certificate_failed\n", 1},
		{"untrusted errors", "ssh-secret-never-store\nHY2TUN_ERROR:remote-secret-do-not-show\n", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newProvisionSSHFixture(t, tc.output, 1)
			m, req := newProvisionTestManager(t, f)
			m.verify = func(context.Context, *hy2Link, net.IP) error {
				t.Error("verification after failed install")
				return nil
			}
			if _, err := m.start(req); err != nil {
				t.Fatal(err)
			}
			trustProvision(t, m, f.key.PublicKey())
			s := terminalProvision(t, m)
			if s.State != "failed" || m.e.store.count() != tc.profiles {
				t.Fatalf("unexpected failure result: %+v, count=%d", s, m.e.store.count())
			}
			if strings.Contains(s.Error, "remote-secret") || strings.Contains(s.Error, req.Password) {
				t.Fatal("remote output exposed in errors")
			}
			if tc.profiles == 1 && s.ServerID == "" {
				t.Fatal("recovery profile unavailable")
			}
		})
	}
}

func TestProvisionVerificationFailureKeepsInstalledProfile(t *testing.T) {
	f := newProvisionSSHFixture(t, "HY2TUN_CONFIGURED\nHY2TUN_READY\n", 0)
	m, req := newProvisionTestManager(t, f)
	m.verify = func(context.Context, *hy2Link, net.IP) error { return errors.New("private UDP failure") }
	if _, err := m.start(req); err != nil {
		t.Fatal(err)
	}
	trustProvision(t, m, f.key.PublicKey())
	s := terminalProvision(t, m)
	if s.State != "succeeded" || s.Warning == "" || strings.Contains(s.Warning, "private UDP failure") || m.e.store.count() != 1 {
		t.Fatalf("UDP failure lost the installed profile: %+v", s)
	}
}

func TestProvisionSingleJobAndCancelBeforeTrust(t *testing.T) {
	f := newProvisionSSHFixture(t, "HY2TUN_READY\n", 0)
	m, req := newProvisionTestManager(t, f)
	if _, err := m.start(req); err != nil {
		t.Fatal(err)
	}
	s := waitProvision(t, m, func(s provisionStatus) bool { return s.State == "awaiting_host_key" })
	if _, err := m.start(req); err == nil {
		t.Fatal("concurrent deployment accepted")
	}
	if _, err := m.action("wrong-id", true, false); err == nil {
		t.Fatal("wrong job accepted")
	}
	if _, err := m.action(s.ID, false, true); err != nil {
		t.Fatal(err)
	}
	s = terminalProvision(t, m)
	if s.State != "failed" || m.e.store.count() != 0 {
		t.Fatalf("cancel result: %+v", s)
	}
	select {
	case <-f.auth:
		t.Fatal("password sent after cancel without trust")
	default:
	}
	if _, err := os.Stat(filepath.Join(m.e.dir, "ssh-host-keys.json")); !os.IsNotExist(err) {
		t.Fatal("cancelled untrusted key persisted")
	}
}

func TestProvisionValidationAndAPIProtection(t *testing.T) {
	f := newProvisionSSHFixture(t, "HY2TUN_READY\n", 0)
	m, valid := newProvisionTestManager(t, f)
	a := &apiServer{e: m.e, token: "test-control-token", deploy: m}
	handler := a.guard(a.serverDeploy)
	call := func(method, body, token, host string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://127.0.0.1/api/servers/deploy", strings.NewReader(body))
		r.Host = host
		r.Header.Set("X-Token", token)
		w := httptest.NewRecorder()
		handler(w, r)
		return w
	}
	for _, auth := range []struct{ token, host string }{{"", "127.0.0.1"}, {a.token, "evil.example"}, {"wrong", "localhost"}} {
		if w := call(http.MethodPost, `{}`, auth.token, auth.host); w.Code != http.StatusForbidden {
			t.Fatalf("unguarded deploy: %d", w.Code)
		}
	}
	if w := call(http.MethodGet, "", a.token, "localhost"); w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"idle"`) || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("initial status: %d %s", w.Code, w.Body.String())
	}
	if w := call(http.MethodPut, `{}`, a.token, "localhost"); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("PUT: %d", w.Code)
	}
	if w := call(http.MethodPost, `{broken`, a.token, "localhost"); w.Code != http.StatusBadRequest {
		t.Fatalf("malformed JSON: %d", w.Code)
	}
	if w := call(http.MethodPost, `{"id":"wrong","trust":true}`, a.token, "localhost"); w.Code != http.StatusConflict {
		t.Fatalf("unknown job action: %d", w.Code)
	}
	for _, change := range []func(*provisionRequest){
		func(r *provisionRequest) { r.Host = "--option" },
		func(r *provisionRequest) { r.Host = "::1" },
		func(r *provisionRequest) { r.SSHPort = 65536 },
		func(r *provisionRequest) { r.Username = "root;id" },
		func(r *provisionRequest) { r.Password = "" },
		func(r *provisionRequest) { r.Domain = "example.com;touch /tmp/bad" },
		func(r *provisionRequest) { r.Domain = "127.0.0.1" },
		func(r *provisionRequest) { r.Email = "Name <owner@example.com>" },
		func(r *provisionRequest) { r.Name = "line\nbreak" },
	} {
		r := valid
		change(&r)
		body, _ := json.Marshal(r)
		if w := call(http.MethodPost, string(body), a.token, "localhost"); w.Code != http.StatusBadRequest {
			t.Fatalf("invalid request accepted: %+v, %d %s", r, w.Code, w.Body.String())
		}
	}
	if m.status().State != "idle" {
		t.Fatal("invalid requests started deployment")
	}
	r := valid
	r.Host = " Example.COM. "
	r.Domain = "Пример.РФ."
	r.Username = ""
	r.SSHPort = 0
	if err := r.normalize(); err != nil || r.Host != "example.com" || r.Domain != "xn--e1afmkfd.xn--p1ai" || r.Username != "root" || r.SSHPort != 22 {
		t.Fatalf("normalization: %+v %v", r, err)
	}
}
