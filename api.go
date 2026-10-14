package main

import (
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

//go:embed ui.html
var uiHTML string

// apiServer is the localhost control surface used by the desktop window, the
// tray and (for the portable build) a normal browser tab.
type apiServer struct {
	e     *Engine
	token string
	srv   *http.Server
}

func newToken() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// startAPI listens on listen (127.0.0.1:port). The token is written to
// <dir>/token so that a local GUI process can find it.
func startAPI(e *Engine, listen string, secure bool) (*apiServer, string, error) {
	a := &apiServer{e: e, token: newToken()}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return nil, "", err
	}
	tokenPath := filepath.Join(e.dir, "token")
	os.MkdirAll(e.dir, 0o755)
	if err := os.WriteFile(tokenPath, []byte(a.token), 0o644); err == nil && secure {
		lockDownFile(tokenPath, true)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", a.page)
	mux.HandleFunc("/api/state", a.guard(a.state))
	mux.HandleFunc("/api/config", a.guard(a.config))
	mux.HandleFunc("/api/processes", a.guard(a.processes))
	mux.HandleFunc("/api/servers", a.guard(a.servers))
	mux.HandleFunc("/api/servers/delete", a.guard(a.serverDelete))
	mux.HandleFunc("/api/servers/share", a.guard(a.serverShare))
	mux.HandleFunc("/api/servers/qr", a.guard(a.serverQR))
	mux.HandleFunc("/api/connect", a.guard(a.connect))
	mux.HandleFunc("/api/disconnect", a.guard(a.disconnect))
	mux.HandleFunc("/api/app", a.guard(a.app))
	mux.HandleFunc("/api/log", a.guard(a.logTail))
	mux.HandleFunc("/api/proxy_ru", a.guard(a.setProxyRU))
	a.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := a.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("api: %v", err)
		}
	}()
	e.startBackground()
	return a, "http://" + ln.Addr().String() + "/?t=" + a.token, nil
}

func (a *apiServer) close() { a.srv.Close() }

// guard rejects requests without the token or with a foreign Host header
// (protects against other web pages and DNS rebinding).
func (a *apiServer) guard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if i := strings.LastIndexByte(host, ':'); i >= 0 {
			host = host[:i]
		}
		if host != "127.0.0.1" && host != "localhost" {
			http.Error(w, "bad host", http.StatusForbidden)
			return
		}
		tok := r.Header.Get("X-Token")
		if tok == "" {
			tok = r.URL.Query().Get("t")
		}
		if tok != a.token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		h(w, r)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

func readJSON(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(v); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func (a *apiServer) state(w http.ResponseWriter, r *http.Request) {
	st := a.e.status()
	writeJSON(w, map[string]any{
		"status":  st,
		"servers": a.e.store.views(st.ServerID),
		"auto":    a.e.store.autoConnect(),
		"lang":    lang(),
	})
}

// config: GET returns the active policy, POST replaces it.
func (a *apiServer) config(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, a.e.router.config())
	case http.MethodPost:
		c := defaultConfig()
		if !readJSON(w, r, &c, 256<<10) {
			return
		}
		saved, err := a.e.router.setConfig(c)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		log.Printf("settings changed: mode=%s apps=%d direct=%d tunnel=%d proxy-ru=%v",
			saved.Mode, len(saved.Apps), len(saved.DirectDomains), len(saved.TunnelDomains), saved.ProxyRU)
		writeJSON(w, saved)
	default:
		http.Error(w, "GET or POST only", http.StatusMethodNotAllowed)
	}
}

// processes lists programs that currently have network sockets, to pick from.
func (a *apiServer) processes(w http.ResponseWriter, r *http.Request) {
	list := a.e.procs.runningWithNetwork()
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	writeJSON(w, list)
}

// servers: GET lists, POST adds a profile from a hy2:// link.
func (a *apiServer) servers(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		writeJSON(w, a.e.store.views(a.e.status().ServerID))
		return
	}
	var body struct {
		Link string `json:"link"`
		Name string `json:"name"`
	}
	if !readJSON(w, r, &body, 8<<10) {
		return
	}
	srv, err := a.e.store.add(body.Link, body.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	log.Printf("server added: %s", srv.Name)
	writeJSON(w, a.e.store.views(a.e.status().ServerID))
}

func (a *apiServer) serverDelete(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if !readJSON(w, r, &body, 1024) {
		return
	}
	st := a.e.status()
	if st.State == stateConnected && st.ServerID == body.ID {
		http.Error(w, tr("сначала отключитесь от этого сервера", "disconnect from this server first"), http.StatusConflict)
		return
	}
	if err := a.e.store.remove(body.ID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, a.e.store.views(st.ServerID))
}

// serverShare returns the full link of one profile so the user can pass it on.
// This is the only place the secret leaves the service, and only on an
// explicit request with the control token.
func (a *apiServer) serverShare(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if !readJSON(w, r, &body, 1024) {
		return
	}
	srv, ok := a.e.store.getExact(body.ID)
	if !ok {
		http.Error(w, tr("сервер не найден", "server not found"), http.StatusNotFound)
		return
	}
	log.Printf("server link shared: %s", srv.Name)
	writeJSON(w, map[string]string{"name": srv.Name, "link": srv.Link})
}

// serverQR renders the link as a QR code (PNG) for scanning with a phone.
func (a *apiServer) serverQR(w http.ResponseWriter, r *http.Request) {
	srv, ok := a.e.store.getExact(r.URL.Query().Get("id"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	png, err := qrcode.Encode(srv.Link, qrcode.Medium, 360)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(png)
}

func (a *apiServer) connect(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if !readJSON(w, r, &body, 1024) {
		return
	}
	if err := a.e.Connect(body.ID); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (a *apiServer) disconnect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	go a.e.Disconnect()
	w.WriteHeader(http.StatusAccepted)
}

// app: application-level options (auto-connect).
func (a *apiServer) app(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AutoConnect *bool   `json:"auto_connect"`
		Lang        *string `json:"lang"`
	}
	if !readJSON(w, r, &body, 1024) {
		return
	}
	if body.AutoConnect != nil {
		a.e.store.setAutoConnect(*body.AutoConnect)
	}
	if body.Lang != nil {
		if l := normalizeLang(*body.Lang); l != "" {
			setLang(l)
			a.e.store.setLangPref(l)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *apiServer) logTail(w http.ResponseWriter, r *http.Request) {
	if a.e.logs == nil {
		writeJSON(w, []string{})
		return
	}
	writeJSON(w, a.e.logs.tail(200))
}

// setProxyRU flips only the .ru switch (kept for simple clients and tests).
func (a *apiServer) setProxyRU(w http.ResponseWriter, r *http.Request) {
	var body struct {
		On bool `json:"on"`
	}
	if !readJSON(w, r, &body, 1024) {
		return
	}
	a.e.router.setProxyRU(body.On)
	log.Printf("setting changed: proxy .ru/.rf through tunnel = %v", body.On)
	w.WriteHeader(http.StatusNoContent)
}

func (a *apiServer) page(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if r.URL.Query().Get("t") != a.token {
		http.Error(w, tr("откройте адрес с токеном, который печатает hy2tun", "open the URL with the token printed by hy2tun"), http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte(uiHTML))
}

var errNoServer = errors.New("no server")
