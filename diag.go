package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// A diagnostics bundle is one zip the user can send to whoever debugs the
// problem: logs, settings (without server passwords), the network state of the
// machine and a live self-test that tells which path (system DNS, the tunnel,
// the direct path, the OS default route) fails for which site.

const diagReadme = `Hy2Tun diagnostics

What is inside:
  summary.json      version, state, counters, firewall state
  config.json       routing settings (apps, domains, modes)
  servers.txt       servers WITHOUT passwords (name, host, port, sni, obfs)
  selftest.txt      live test of a few sites over every path
  log-current.txt   service log (last part)       log-old.txt  the previous one
  decisions.json    the latest routing decisions (process -> tunnel/direct)
  network/*.txt     adapters, routes, DNS, proxy, firewall, VPN software

Server passwords and hy2:// links are removed. The logs and decisions can contain
the names of sites you visited while "Detailed logging" was on; look at them
before sending if that matters to you.
`

var secretRE = regexp.MustCompile(`hy2://[^\s"'<>]+`)

func redact(s string) string { return secretRE.ReplaceAllString(s, "hy2://<redacted>") }

// readTokenFile returns the control token stored in dir ("" if there is none).
func readTokenFile(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "token"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func tailFile(path string, max int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	if st.Size() > max {
		f.Seek(st.Size()-max, io.SeekStart)
	}
	b, _ := io.ReadAll(f)
	s := string(b)
	if st.Size() > max {
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
		s = "[... earlier part cut ...]\n" + s
	}
	return redact(s)
}

type diagCmd struct {
	name string
	argv []string
}

// buildDiag assembles the bundle. e is nil when the service is not running; then
// only the files and the network snapshot are collected.
func buildDiag(e *Engine, dir string) ([]byte, error) {
	var mu sync.Mutex
	files := map[string]string{}
	add := func(name, text string) {
		mu.Lock()
		files[name] = text
		mu.Unlock()
	}
	add("README.txt", diagReadme)

	// the self-test and the OS commands run in parallel, they take a while
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if e != nil {
			add("selftest.txt", e.selfTest())
		} else {
			add("selftest.txt", "the service is not running: no live test possible\n")
		}
	}()
	for _, c := range diagCommands() {
		c := c
		wg.Add(1)
		go func() {
			defer wg.Done()
			add("network/"+c.name+".txt", redact(diagRun(25*time.Second, c.argv)))
		}()
	}

	sum := map[string]any{
		"time":     time.Now().Format(time.RFC3339),
		"version":  appVersion,
		"build":    selfBuildID(),
		"os":       runtime.GOOS + "/" + runtime.GOARCH,
		"admin":    isAdmin(),
		"data_dir": dir,
		"service":  e != nil,
	}
	var store *serverStore
	if e != nil {
		st := e.status()
		recent := st.Recent
		st.Recent = nil
		sum["status"] = st
		sum["firewall"] = e.firewallInfo()
		sum["debug_logging"] = e.store.debug()
		sum["auto_connect"] = e.store.autoConnect()
		if b, err := json.MarshalIndent(e.router.config(), "", "  "); err == nil {
			add("config.json", string(b))
		}
		if b, err := json.MarshalIndent(recent, "", "  "); err == nil {
			add("decisions.json", redact(string(b)))
		}
		store = e.store
		if e.logs != nil {
			add("log-ring.txt", redact(strings.Join(e.logs.tail(500), "\n")))
		}
	} else {
		if b, err := os.ReadFile(filepath.Join(dir, "settings.json")); err == nil {
			add("config.json", string(b))
		}
		store = newServerStore(dir, false)
	}
	var sb strings.Builder
	for _, v := range store.views("") {
		fmt.Fprintf(&sb, "%s | %s:%s | sni=%s obfs=%s hop=%s\n", v.Name, v.Host, v.Port, v.SNI, v.Obfs, v.Hop)
	}
	add("servers.txt", sb.String())
	if b, err := json.MarshalIndent(sum, "", "  "); err == nil {
		add("summary.json", string(b))
	}
	add("log-current.txt", tailFile(filepath.Join(dir, "hy2tun.log"), 3<<20))
	add("log-old.txt", tailFile(filepath.Join(dir, "hy2tun.log.old"), 2<<20))
	wg.Wait()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		w, err := zw.Create("hy2tun-diag/" + n)
		if err != nil {
			return nil, err
		}
		w.Write([]byte(files[n]))
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

var diagHosts = []string{"github.com", "gemini.google.com", "www.google.com", "www.cloudflare.com", "ya.ru", "example.com"}

// selfTest tries a few sites over every path the program uses and reports which
// step fails: name resolution, the tunnel, the direct path or the OS route.
func (e *Engine) selfTest() string {
	e.mu.Lock()
	s := e.cur
	state := e.state
	e.mu.Unlock()
	res := make([]string, len(diagHosts))
	var wg sync.WaitGroup
	for i, h := range diagHosts {
		i, h := i, h
		wg.Add(1)
		go func() {
			defer wg.Done()
			res[i] = testHost(h, s)
		}()
	}
	wg.Wait()
	head := fmt.Sprintf("state: %s (tunnel session: %v)\n\n", state, s != nil)
	return head + strings.Join(res, "\n")
}

func testHost(host string, s *session) string {
	var b strings.Builder
	fmt.Fprintf(&b, "== %s\n", host)
	t0 := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	cancel()
	var v4, v6 string
	if err != nil {
		fmt.Fprintf(&b, "  system DNS             FAIL %v (%v)\n", err, time.Since(t0).Round(time.Millisecond))
	} else {
		var list []string
		for _, a := range addrs {
			list = append(list, a.IP.String())
			if a.IP.To4() != nil && v4 == "" {
				v4 = a.IP.String()
			} else if a.IP.To4() == nil && v6 == "" {
				v6 = a.IP.String()
			}
		}
		fmt.Fprintf(&b, "  system DNS             ok   %v (%v)\n", list, time.Since(t0).Round(time.Millisecond))
	}

	probe := func(label string, dial func(context.Context) (net.Conn, error)) {
		t := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 9*time.Second)
		defer cancel()
		c, err := dial(ctx)
		if err != nil {
			fmt.Fprintf(&b, "  %-22s FAIL connect: %v (%v)\n", label, err, time.Since(t).Round(time.Millisecond))
			return
		}
		defer c.Close()
		tc := tls.Client(c, &tls.Config{ServerName: host})
		tc.SetDeadline(time.Now().Add(8 * time.Second))
		tconn := time.Since(t)
		if err := tc.Handshake(); err != nil {
			fmt.Fprintf(&b, "  %-22s FAIL tls handshake: %v (connect %v, total %v)\n", label, err, tconn.Round(time.Millisecond), time.Since(t).Round(time.Millisecond))
			return
		}
		thand := time.Since(t)
		fmt.Fprintf(&b, "  %-22s ", label)
		fmt.Fprintf(tc, "HEAD / HTTP/1.1\r\nHost: %s\r\nConnection: close\r\nUser-Agent: hy2tun-diag\r\n\r\n", host)
		line, err := bufio.NewReader(tc).ReadString('\n')
		if err != nil && line == "" {
			fmt.Fprintf(&b, "FAIL no http answer: %v (connect %v, tls %v)\n", err, tconn.Round(time.Millisecond), thand.Round(time.Millisecond))
			return
		}
		fmt.Fprintf(&b, "ok   %s (connect %v, tls %v, total %v)\n", strings.TrimSpace(line), tconn.Round(time.Millisecond), thand.Round(time.Millisecond), time.Since(t).Round(time.Millisecond))
	}

	if s != nil {
		probe("via tunnel (hysteria)", func(ctx context.Context) (net.Conn, error) {
			return s.box.TCP(net.JoinHostPort(host, "443"))
		})
		if s.direct != nil && v4 != "" {
			probe("direct (physical NIC)", func(ctx context.Context) (net.Conn, error) {
				return s.direct.dialTCP(ctx, net.JoinHostPort(v4, "443"))
			})
		}
	}
	probe("OS default, IPv4", func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp4", net.JoinHostPort(host, "443"))
	})
	if v6 != "" {
		probe("OS default, IPv6", func(ctx context.Context) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp6", net.JoinHostPort(v6, "443"))
		})
	}
	return b.String()
}

// diag is the control API endpoint. Plain GET returns the bundle as a zip download;
// ?save=1 stores it as a file in <data dir>/diagnostics and answers with its path, so
// there is always a real file to find and send, whatever the window can or cannot download.
func (a *apiServer) diag(w http.ResponseWriter, r *http.Request) {
	data, err := buildDiag(a.e, a.e.dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if r.URL.Query().Get("save") == "1" {
		dir := filepath.Join(a.e.dir, "diagnostics")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		path := filepath.Join(dir, diagName())
		if err := os.WriteFile(path, data, 0o644); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		pruneDiag(dir, 5)
		log.Printf("diagnostics saved: %s", path)
		writeJSON(w, map[string]any{"path": path, "size": len(data)})
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+diagName()+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Write(data)
}

// pruneDiag keeps the newest n bundles.
func pruneDiag(dir string, n int) {
	list, _ := filepath.Glob(filepath.Join(dir, "hy2tun-diag-*.zip"))
	sort.Strings(list)
	for len(list) > n {
		os.Remove(list[0])
		list = list[1:]
	}
}

func diagName() string { return "hy2tun-diag-" + time.Now().Format("20060102-150405") + ".zip" }

// cmdDiag: `hy2tun diag [file.zip]` asks the running service for the bundle (so the
// self-test sees the live tunnel); without a service it collects what it can.
func cmdDiag(args []string) {
	c, fs := parseFlags(args)
	dir, _ := resolveDataDir(c.data)
	out := fs.Arg(0)
	if out == "" {
		home, _ := os.UserHomeDir()
		out = filepath.Join(home, "Desktop")
		if st, err := os.Stat(out); err != nil || !st.IsDir() {
			out = "."
		}
		out = filepath.Join(out, diagName())
	}
	fmt.Println(tr("Собираем диагностику (до ~20 секунд)...", "Collecting diagnostics (up to ~20 seconds)..."))
	var data []byte
	for _, d := range tokenDirs(dir) {
		tok := readTokenFile(d)
		if tok == "" {
			continue
		}
		req, _ := http.NewRequest("GET", "http://"+c.ui+"/api/diag", nil)
		req.Header.Set("X-Token", tok)
		resp, err := (&http.Client{Timeout: 90 * time.Second}).Do(req)
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 200 {
			data = b
			break
		}
	}
	if data == nil {
		fmt.Println(tr("служба не отвечает: собираем без живого теста", "the service does not answer: collecting without the live test"))
		var err error
		if data, err = buildDiag(nil, dir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	if err := os.WriteFile(out, data, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(tr("Готово: ", "Done: ") + out)
}
