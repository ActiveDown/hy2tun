package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type apiHarness struct {
	t     *testing.T
	e     *Engine
	base  string
	token string
	dir   string
}

func newAPIHarness(t *testing.T) *apiHarness {
	dir := t.TempDir()
	e := newEngine(defaultEngineOpts(), dir, false, newRingLog(50))
	a, u, err := startAPI(e, "127.0.0.1:0", false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.close)
	i := strings.Index(u, "/?t=")
	return &apiHarness{t: t, e: e, base: u[:i], token: u[i+4:], dir: dir}
}

func (h *apiHarness) do(method, path, body string) (*http.Response, []byte) {
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, h.base+path, r)
	req.Header.Set("X-Token", h.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, b
}

func TestAPIAuthAndPage(t *testing.T) {
	h := newAPIHarness(t)
	r, _ := http.Get(h.base + "/api/state")
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("no token: %d", r.StatusCode)
	}
	r, _ = http.Get(h.base + "/?t=" + h.token)
	b, _ := io.ReadAll(r.Body)
	if r.StatusCode != 200 || !strings.Contains(string(b), "Hy2Tun") || !strings.Contains(string(b), "power") {
		t.Fatalf("page: %d", r.StatusCode)
	}
	// wrong Host header (DNS rebinding) -> forbidden
	req, _ := http.NewRequest("GET", h.base+"/api/state?t="+h.token, nil)
	req.Host = "evil.example"
	r, _ = http.DefaultClient.Do(req)
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("host check: %d", r.StatusCode)
	}
	// the token file for the GUI
	if b, err := os.ReadFile(filepath.Join(h.dir, "token")); err != nil || string(b) != h.token {
		t.Fatalf("token file: %q %v", b, err)
	}
}

func TestAPIServersLifecycle(t *testing.T) {
	h := newAPIHarness(t)
	link := "hy2://Admin:secret@203.0.113.10:443?sni=example.com#Мой сервер"
	body, _ := json.Marshal(map[string]string{"link": link})
	resp, b := h.do("POST", "/api/servers", string(body))
	if resp.StatusCode != 200 {
		t.Fatalf("add: %d %s", resp.StatusCode, b)
	}
	if strings.Contains(string(b), "secret") {
		t.Fatal("the password must never be returned by the API")
	}
	var views []ServerView
	json.Unmarshal(b, &views)
	if len(views) != 1 || views[0].Name != "Мой сервер" || views[0].Host != "203.0.113.10" || views[0].SNI != "example.com" {
		t.Fatalf("views: %+v", views)
	}
	// duplicates are ignored
	h.do("POST", "/api/servers", string(body))
	if h.e.store.count() != 1 {
		t.Fatal("duplicate link was added twice")
	}
	// bad links are rejected
	if resp, _ := h.do("POST", "/api/servers", `{"link":"http://nope"}`); resp.StatusCode != 400 {
		t.Fatalf("bad link: %d", resp.StatusCode)
	}
	if resp, _ := h.do("POST", "/api/servers", `{"link":"hy2://example.com:443"}`); resp.StatusCode != 400 {
		t.Fatalf("link without password: %d", resp.StatusCode)
	}
	// the stored file keeps the secret, state does not
	if raw, _ := os.ReadFile(filepath.Join(h.dir, "servers.json")); !strings.Contains(string(raw), "secret") {
		t.Fatal("servers.json should store the link")
	}
	_, st := h.do("GET", "/api/state", "")
	if strings.Contains(string(st), "secret") || !strings.Contains(string(st), `"state":"disconnected"`) {
		t.Fatalf("state: %s", st)
	}
	// delete
	resp, _ = h.do("POST", "/api/servers/delete", `{"id":"`+views[0].ID+`"}`)
	if resp.StatusCode != 200 || h.e.store.count() != 0 {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
}

func TestAPIConfigAndAuto(t *testing.T) {
	h := newAPIHarness(t)
	resp, b := h.do("POST", "/api/config", `{"mode":"selected","apps":["chrome.exe"],"include_children":true,"proxy_ru":false,"geoip_ru":true,"vpn_clients_direct":true}`)
	if resp.StatusCode != 200 {
		t.Fatalf("config: %d %s", resp.StatusCode, b)
	}
	c := h.e.router.config()
	if c.Mode != modeSelected || len(c.Apps) != 1 || c.Apps[0] != "chrome.exe" {
		t.Fatalf("not applied: %+v", c)
	}
	if raw, _ := os.ReadFile(filepath.Join(h.dir, "settings.json")); !strings.Contains(string(raw), "chrome.exe") {
		t.Fatal("settings were not persisted")
	}
	if resp, _ := h.do("POST", "/api/config", `{"mode":"bogus"}`); resp.StatusCode != 400 {
		t.Fatalf("bad mode: %d", resp.StatusCode)
	}
	// simple switch kept for old clients
	if resp, _ := h.do("POST", "/api/proxy_ru", `{"on":true}`); resp.StatusCode != http.StatusNoContent || !h.e.router.proxyRU() {
		t.Fatalf("proxy_ru: %d", resp.StatusCode)
	}
	// auto-connect flag
	if resp, _ := h.do("POST", "/api/app", `{"auto_connect":true}`); resp.StatusCode != http.StatusNoContent || !h.e.store.autoConnect() {
		t.Fatalf("auto: %d", resp.StatusCode)
	}
	// connecting without any server explains why
	h2 := newAPIHarness(t)
	if resp, b := h2.do("POST", "/api/connect", `{"id":""}`); resp.StatusCode != http.StatusConflict || !strings.Contains(string(b), "hy2://") {
		t.Fatalf("connect without servers: %d %s", resp.StatusCode, b)
	}
}

func TestStoreMigrationAndRemoveLast(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "hy2.url"), []byte("hy2://u:p@1.2.3.4:443?sni=x.org#old\n"), 0o600)
	e := newEngine(defaultEngineOpts(), dir, false, nil)
	migrateLegacyLink(e)
	if e.store.count() != 1 {
		t.Fatal("hy2.url was not imported")
	}
	s, _ := e.store.get("")
	if s.Name != "old" {
		t.Fatalf("name: %q", s.Name)
	}
	// reload from disk
	e2 := newEngine(defaultEngineOpts(), dir, false, nil)
	if e2.store.count() != 1 {
		t.Fatal("store did not persist")
	}
}

func TestRingLog(t *testing.T) {
	r := newRingLog(3)
	r.Write([]byte("a\nb\nc\nd\npart"))
	r.Write([]byte("ial\n"))
	got := r.tail(10)
	if len(got) != 3 || got[2] != "partial" || got[0] != "c" {
		t.Fatalf("ring: %v", got)
	}
}

func TestAPIShareAndQR(t *testing.T) {
	h := newAPIHarness(t)
	link := "hy2://Admin:secret@203.0.113.10:443?sni=example.com#Home"
	srv, err := h.e.store.add(link, "")
	if err != nil {
		t.Fatal(err)
	}
	// share returns the full link on an explicit request
	resp, b := h.do("POST", "/api/servers/share", `{"id":"`+srv.ID+`"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("share: %d %s", resp.StatusCode, b)
	}
	var got map[string]string
	json.Unmarshal(b, &got)
	if got["link"] != link || got["name"] != "Home" {
		t.Fatalf("share payload: %v", got)
	}
	// unknown / empty ids never fall back to "the last server"
	if resp, _ := h.do("POST", "/api/servers/share", `{"id":""}`); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("empty id: %d", resp.StatusCode)
	}
	if resp, _ := h.do("POST", "/api/servers/share", `{"id":"nope"}`); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown id: %d", resp.StatusCode)
	}
	// QR is a PNG, reachable with the token in the query (used by <img src>)
	r, err := http.Get(h.base + "/api/servers/qr?id=" + srv.ID + "&t=" + h.token)
	if err != nil {
		t.Fatal(err)
	}
	png, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != 200 || r.Header.Get("Content-Type") != "image/png" || len(png) < 100 || string(png[1:4]) != "PNG" {
		t.Fatalf("qr: %d %s len=%d", r.StatusCode, r.Header.Get("Content-Type"), len(png))
	}
	// without a token neither endpoint answers
	if r, _ := http.Get(h.base + "/api/servers/qr?id=" + srv.ID); r.StatusCode != http.StatusForbidden {
		t.Fatalf("qr without token: %d", r.StatusCode)
	}
}

func TestLanguageDefaultsToEnglish(t *testing.T) {
	t.Cleanup(func() { setLang("en") })
	h := newAPIHarness(t)
	state := func() map[string]any {
		_, b := h.do("GET", "/api/state", "")
		var m map[string]any
		json.Unmarshal(b, &m)
		return m
	}
	if state()["lang"] != "en" {
		t.Fatalf("default language must be English, got %v", state()["lang"])
	}
	// errors are English by default
	resp, b := h.do("POST", "/api/connect", `{"id":""}`)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(b), "no saved server") {
		t.Fatalf("english error expected: %d %s", resp.StatusCode, b)
	}
	// Russian only on explicit request, and it is remembered
	if resp, _ := h.do("POST", "/api/app", `{"lang":"ru"}`); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("set lang: %d", resp.StatusCode)
	}
	if state()["lang"] != "ru" {
		t.Fatal("language switch not applied")
	}
	if _, b := h.do("POST", "/api/connect", `{"id":""}`); !strings.Contains(string(b), "нет сохранённого сервера") {
		t.Fatalf("russian error expected: %s", b)
	}
	// setting auto-connect must not touch the language and vice versa
	h.do("POST", "/api/app", `{"auto_connect":true}`)
	if state()["lang"] != "ru" || !h.e.store.autoConnect() {
		t.Fatal("partial /api/app updates must not reset other fields")
	}
	// a new engine on the same data dir comes up in the saved language ...
	setLang("en")
	e2 := newEngine(defaultEngineOpts(), h.dir, false, nil)
	if lang() != "ru" || e2.store.getLang() != "ru" {
		t.Fatalf("saved language not restored: %s", lang())
	}
	// ... and an empty profile always starts in English
	setLang("ru")
	newEngine(defaultEngineOpts(), t.TempDir(), false, nil)
	if lang() != "en" {
		t.Fatalf("fresh install must be English, got %s", lang())
	}
	// garbage is ignored
	h.do("POST", "/api/app", `{"lang":"xx"}`)
}

func TestDiagBundleOffline(t *testing.T) {
	dir := t.TempDir()
	link := "hy2://supersecret@example.com:443?sni=example.com#Test"
	os.WriteFile(dir+"/servers.json", []byte(`{"servers":[{"id":"a1","name":"Test","link":"`+link+`"}],"last":"a1"}`), 0o600)
	os.WriteFile(dir+"/hy2tun.log", []byte("connecting "+link+"\nok\n"), 0o600)
	data, err := buildDiag(nil, dir)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var all strings.Builder
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		all.Write(b)
	}
	for _, n := range []string{"hy2tun-diag/README.txt", "hy2tun-diag/summary.json", "hy2tun-diag/servers.txt", "hy2tun-diag/log-current.txt", "hy2tun-diag/selftest.txt"} {
		if !names[n] {
			t.Errorf("missing %s", n)
		}
	}
	if strings.Contains(all.String(), "supersecret") {
		t.Error("the server password leaked into the bundle")
	}
	if !strings.Contains(all.String(), "example.com:443") {
		t.Error("server host missing from servers.txt")
	}
}
