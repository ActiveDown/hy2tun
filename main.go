// hy2tun - Hysteria2 client with a global TUN, per-app rules and a Windows service.
//
// Commands:
//
//	hy2tun [run] [flags]    run in the foreground (portable / debugging)
//	hy2tun service          run as a Windows service (started by the SCM)
//	hy2tun install          install + start the service (administrator)
//	hy2tun uninstall        stop + remove the service (administrator)
//	hy2tun gui              desktop window + tray icon (normal user)
//	hy2tun dry [flags]      connect to the server and exit (no TUN)
//	hy2tun diag [file.zip]  collect a diagnostics bundle (logs, network state, live test)
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

// appVersion is overridden at build time: -ldflags "-X main.appVersion=1.2.3"
var appVersion = "0.8.0"

type commonFlags struct {
	data      string
	link      string
	name      string
	mtu       int
	dns       string
	directDNS string
	ui        string
	verbose   bool
	blockQUIC bool
	noOpen    bool
	proxyRU   bool
	dry       bool
}

func parseFlags(args []string) (*commonFlags, *flag.FlagSet) {
	fs := flag.NewFlagSet("hy2tun", flag.ExitOnError)
	c := &commonFlags{}
	fs.StringVar(&c.data, "data", "", "data directory (default: next to the exe in portable mode, else %ProgramData%\\Hy2Tun)")
	fs.StringVar(&c.link, "link", "", "hy2:// link or a file containing it (imported into the server list)")
	fs.StringVar(&c.name, "name", "Hy2Tun", "TUN adapter name")
	fs.IntVar(&c.mtu, "mtu", 1380, "TUN MTU")
	fs.StringVar(&c.dns, "dns", "1.1.1.1,8.8.8.8", "DNS servers set on the TUN adapter (resolved through the tunnel)")
	fs.StringVar(&c.directDNS, "direct-dns", "77.88.8.8:53", "resolver used directly (physical NIC) for RU names")
	fs.StringVar(&c.ui, "ui", "127.0.0.1:7878", "control API / panel address")
	fs.BoolVar(&c.verbose, "v", false, "log every flow")
	fs.BoolVar(&c.blockQUIC, "block-quic", true, "drop UDP/443 so browsers use TCP through the tunnel")
	fs.BoolVar(&c.noOpen, "no-open", false, "do not open the panel in the browser")
	fs.BoolVar(&c.proxyRU, "proxy-ru", false, "send .ru/.rf through the tunnel too; overrides the saved setting")
	fs.BoolVar(&c.dry, "dry", false, "connect to the server and exit (no TUN)")
	var rest []string
	for _, a := range args {
		if a != "--tray" && a != "-tray" {
			rest = append(rest, a)
		}
	}
	fs.Parse(rest)
	return c, fs
}

func (c *commonFlags) engineOpts() EngineOpts {
	o := defaultEngineOpts()
	o.TunName, o.MTU, o.DirectDNS, o.Verbose, o.BlockQUIC = c.name, c.mtu, c.directDNS, c.verbose, c.blockQUIC
	if c.dns != "" {
		o.DNS = strings.Split(c.dns, ",")
	}
	return o
}

// importLink adds the -link value (a hy2:// URL or a file with one) to the store.
func importLink(e *Engine, v string) {
	if v == "" {
		return
	}
	if !strings.Contains(v, "://") {
		b, err := os.ReadFile(v)
		if err != nil {
			log.Printf("warn: -link: %v", err)
			return
		}
		v = string(b)
	}
	if _, err := e.store.add(v, ""); err != nil {
		log.Printf("warn: -link: %v", err)
	}
}

// migrateLegacyLink imports hy2.url (older test stands) once.
func migrateLegacyLink(e *Engine) {
	if e.store.count() > 0 {
		return
	}
	if b, err := os.ReadFile(filepath.Join(e.dir, "hy2.url")); err == nil {
		if _, err := e.store.add(string(b), ""); err == nil {
			log.Printf("imported hy2.url into the server list")
		}
	}
}

// rotatingFile is the log file with a size cap: past maxLogSize it becomes
// <name>.old and a new file is started, so detailed logging cannot fill the disk.
type rotatingFile struct {
	mu   sync.Mutex
	path string
	f    *os.File
	size int64
}

const maxLogSize = 8 << 20

func openRotating(path string) (*rotatingFile, error) {
	r := &rotatingFile{path: path}
	if st, err := os.Stat(path); err == nil && st.Size() > maxLogSize/2 {
		os.Rename(path, path+".old")
	}
	return r, r.open()
}

func (r *rotatingFile) open() error {
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	r.f = f
	if st, err := f.Stat(); err == nil {
		r.size = st.Size()
	}
	return nil
}

func (r *rotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size+int64(len(p)) > maxLogSize {
		r.f.Close()
		os.Rename(r.path, r.path+".old")
		if err := r.open(); err != nil {
			return len(p), nil
		}
	}
	n, err := r.f.Write(p)
	r.size += int64(n)
	return n, err
}

func (r *rotatingFile) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.f.Close()
}

func setupLogging(dir string, toFile bool) (*ringLog, func()) {
	ring := newRingLog(500)
	var w io.Writer = ring
	closeFn := func() {}
	if toFile {
		os.MkdirAll(dir, 0o755)
		if f, err := openRotating(filepath.Join(dir, "hy2tun.log")); err == nil {
			w = io.MultiWriter(ring, f)
			closeFn = func() { f.Close() }
		}
	} else {
		w = io.MultiWriter(ring, os.Stderr)
	}
	log.SetOutput(w)
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	return ring, closeFn
}

func main() {
	args := os.Args[1:]
	cmd := "run"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	} else if len(args) == 0 && runtime.GOOS == "darwin" && !isAdmin() {
		if exe, err := os.Executable(); err == nil && !strings.Contains(exe, "Hy2Tun.app") {
			appPath := filepath.Join(filepath.Dir(exe), "Hy2Tun.app")
			if _, err := os.Stat(appPath); err == nil && os.Getenv("HY2TUN_NO_DETACH") != "1" {
				exec.Command("open", appPath).Start()
				os.Exit(0)
			}
		}
		cmd = "gui"
	}
	switch cmd {
	case "run":
		cmdRun(args)
	case "dry":
		cmdRun(append([]string{"-dry"}, args...))
	case "api":
		cmdAPI(args)
	case "service":
		cmdService(args)
	case "install":
		cmdInstall(args)
	case "uninstall":
		cmdUninstall(args)
	case "gui":
		cmdGUI(args)
	case "diag":
		cmdDiag(args)
	case "version":
		fmt.Println("hy2tun", appVersion)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", cmd)
		os.Exit(2)
	}
}

// cmdRun is the foreground mode: engine + API in this process, Ctrl+C stops.
func cmdRun(args []string) {
	c, fs := parseFlags(args)
	dir, portable := resolveDataDir(c.data)
	ring, closeLog := setupLogging(dir, false)
	defer closeLog()
	e := newEngine(c.engineOpts(), dir, !portable, ring)
	importLink(e, c.link)
	migrateLegacyLink(e)
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "proxy-ru" {
			e.router.setProxyRU(c.proxyRU)
		}
	})
	srv, ok := e.store.get("")
	if !ok {
		log.Println(tr("нет сохранённого сервера: открываем панель управления в браузере (добавьте ссылку hy2://)...", "no saved server: opening control panel in browser (add a hy2:// link)..."))
		cmdAPI(args)
		return
	}

	if c.dry {
		dryRun(srv)
		return
	}
	if !isAdmin() {
		if runtime.GOOS == "darwin" {
			log.Println(tr("для создания TUN-интерфейса требуются права администратора (запустите через sudo).", "administrator rights are required to create the TUN interface (run with sudo)."))
			log.Println(tr("открываем панель управления...", "opening control panel..."))
			cmdAPI(args)
			return
		} else {
			log.Fatal("administrator rights are required (use 1-run-as-admin.bat)")
		}
	}

	api, url, err := startAPI(e, c.ui, !portable)
	if err != nil {
		log.Printf("warn: control panel: %v", err)
	} else {
		defer api.close()
		log.Printf("control panel: %s", url)
	}
	if err := e.Connect(srv.ID); err != nil {
		log.Fatal(err)
	}
	// wait for the connection result before opening the panel
	for i := 0; i < 600; i++ {
		if st := e.status().State; st != stateConnecting {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if st := e.status(); st.State != stateConnected {
		log.Fatalf(tr("не удалось подключиться: %s", "connection failed: %s"), st.Error)
	}
	log.Printf("connected: traffic goes through %s. proxy .ru/.rf = %v. Ctrl+C to stop.", srv.Name, e.router.proxyRU())
	if api != nil && !c.noOpen {
		openBrowser(url)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-sig:
			log.Printf("stopping...")
			e.Shutdown()
			return
		case <-tick.C:
			st := e.status()
			log.Printf("tcp flows=%d (failed %d)  udp flows=%d  open=%d  up %.1f KB/s  down %.1f KB/s",
				st.TCPFlows, st.TCPFailed, st.UDPFlows, st.OpenFlows, st.UpBps/1024, st.DownBps/1024)
		}
	}
}

func dryRun(srv Server) {
	l, err := parseLink(srv.Link)
	if err != nil {
		log.Fatal(err)
	}
	ip, err := resolveServer(l.Host)
	if err != nil {
		log.Fatal(err)
	}
	if rt, err := bestRoute(ip); err == nil {
		log.Printf("server %s via interface %s (%s), gateway %s", ip, rt.IfIndex, rt.Desc, rt.NextHop)
	}
	hc, err := newHysteriaClient(l, ip, false)
	if err != nil {
		log.Fatalf("hysteria2 connect: %v", err)
	}
	defer hc.Close()
	conn, err := hc.TCP("example.com:80")
	if err != nil {
		log.Fatalf("dry-run TCP through server failed: %v", err)
	}
	fmt.Fprint(conn, "HEAD / HTTP/1.0\r\nHost: example.com\r\n\r\n")
	buf := make([]byte, 64)
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	n, _ := conn.Read(buf)
	conn.Close()
	log.Printf("dry-run OK, server answered: %q", strings.SplitN(string(buf[:n]), "\r\n", 2)[0])
}

// cmdAPI serves only the control API/UI without touching the network. It needs
// no administrator rights and is handy for UI development and tests.
func cmdAPI(args []string) {
	c, _ := parseFlags(args)
	dir, portable := resolveDataDir(c.data)
	ring, closeLog := setupLogging(dir, false)
	defer closeLog()
	e := newEngine(c.engineOpts(), dir, !portable, ring)
	importLink(e, c.link)
	migrateLegacyLink(e)
	api, url, err := startAPI(e, c.ui, !portable)
	if err != nil {
		log.Fatal(err)
	}
	defer api.close()
	log.Printf("control panel: %s", url)
	if !c.noOpen {
		openBrowser(url)
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
}

func openBrowser(url string) {
	switch runtime.GOOS {
	case "darwin":
		exec.Command("open", url).Start()
	case "windows":
		exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		exec.Command("xdg-open", url).Start()
	}
}
