package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func adminTestServer(t *testing.T, h *apiHarness) string {
	t.Helper()
	link := "hy2://Admin:secret@203.0.113.10:443?sni=example.com#Test server"
	resp, body := h.do("POST", "/api/servers", `{"link":`+jsonQuote(t, link)+`}`)
	if resp.StatusCode != 200 {
		t.Fatalf("server add: %d %s", resp.StatusCode, body)
	}
	var list []ServerView
	if _, body := h.do("GET", "/api/servers", ""); json.Unmarshal(body, &list) != nil || len(list) == 0 {
		t.Fatal("no server in the list")
	}
	id := list[0].ID
	if list[0].Admin {
		t.Fatal("a fresh server must not carry the admin flag")
	}
	return id
}

func TestAdminAPINeedsAttachedServer(t *testing.T) {
	h := newAPIHarness(t)
	r, _ := http.Get(h.base + "/api/admin/status?id=x&t=" + h.token)
	if r.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown server: %d", r.StatusCode)
	}

	id := adminTestServer(t, h)

	resp, body := h.do("GET", "/api/admin/status?id="+id, "")
	if resp.StatusCode != 200 {
		t.Fatalf("status: %d %s", resp.StatusCode, body)
	}
	var st map[string]any
	if err := json.Unmarshal(body, &st); err != nil {
		t.Fatal(err)
	}
	if st["attached"] != false {
		t.Fatalf("fresh server must not be attached: %v", st)
	}

	resp, body = h.do("POST", "/api/admin/userlink", `{"id":"`+id+`","user":"bob"}`)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(string(body), "SSH") {
		t.Fatalf("userlink without attach: %d %s", resp.StatusCode, body)
	}

	resp, body = h.do("POST", "/api/admin/detach", `{"id":"`+id+`"}`)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("detach on an unattached server: %d %s", resp.StatusCode, body)
	}
}

func TestAdminAPIAttachValidation(t *testing.T) {
	h := newAPIHarness(t)
	id := adminTestServer(t, h)

	resp, body := h.do("POST", "/api/admin/attach", `{"id":"`+id+`","host":"203.0.113.9","ssh_port":70000,"username":"root","password":"pw"}`)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "port") {
		t.Fatalf("bad port accepted: %d %s", resp.StatusCode, body)
	}
	resp, body = h.do("POST", "/api/admin/attach", `{"id":"`+id+`","host":"203.0.113.9","ssh_port":22,"username":"bad id!","password":"pw"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad username accepted: %d %s", resp.StatusCode, body)
	}
	resp, body = h.do("POST", "/api/admin/attach", `{"id":"`+id+`","host":"203.0.113.9","ssh_port":22,"username":"root"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty password accepted: %d %s", resp.StatusCode, body)
	}
}

func TestAdminAPIUserValidation(t *testing.T) {
	if err := adminValidateUser("Admin", "x"); err == nil {
		t.Fatal("Admin must not be manageable")
	}
	if err := adminValidateUser("bad id!", "x"); err == nil {
		t.Fatal("invalid username accepted")
	}
	if err := adminValidateUser("bob", strings.Repeat("x", 129)); err == nil {
		t.Fatal("long password accepted")
	}
	if err := adminValidateUser("bob-1_2", "topsecret"); err != nil {
		t.Fatalf("valid user rejected: %v", err)
	}
}

func TestAdminAPIServerDeleteForgetsAccess(t *testing.T) {
	h := newAPIHarness(t)
	id := adminTestServer(t, h)
	if err := h.e.admin.set(id, adminCredential{Host: "203.0.113.9", Port: 22, Username: "root", Password: "pw"}); err != nil {
		t.Fatal(err)
	}
	var list []ServerView
	resp, body := h.do("GET", "/api/servers", "")
	_ = resp
	_ = body
	if json.Unmarshal(body, &list) != nil || !list[0].Admin {
		t.Fatal("attached server must show the admin flag")
	}
	resp, body = h.do("POST", "/api/servers/delete", `{"id":"`+id+`"}`)
	if resp.StatusCode != 200 {
		t.Fatalf("delete: %d %s", resp.StatusCode, body)
	}
	if h.e.admin.has(id) {
		t.Fatal("the SSH access must be forgotten with the server")
	}
}

// jsonQuote renders a string as a JSON string for inline test bodies.
func jsonQuote(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
