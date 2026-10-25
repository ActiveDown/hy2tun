package main

import (
	"errors"
	"fmt"
	"log"
	"net"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/apernet/hysteria/core/v2/client"
	"golang.zx2c4.com/wireguard/tun"
)

// Connection states reported to the UI.
const (
	stateDisconnected = "disconnected"
	stateConnecting   = "connecting"
	stateConnected    = "connected"
	stateError        = "error"
)

type EngineOpts struct {
	TunName   string
	MTU       int
	DNS       []string
	DirectDNS string
	Verbose   bool
	BlockQUIC bool
}

func defaultEngineOpts() EngineOpts {
	name := "Hy2Tun"
	if runtime.GOOS == "darwin" {
		name = "utun"
	}
	return EngineOpts{TunName: name, MTU: 1380, DNS: []string{"1.1.1.1", "8.8.8.8"}, DirectDNS: "77.88.8.8:53", BlockQUIC: true}
}

// hcBox lets the Hysteria2 client be replaced while flows keep a stable handle
// (needed after the physical network changed).
type hcBox struct {
	mu sync.RWMutex
	c  client.Client
}

func (b *hcBox) cur() client.Client {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.c
}
func (b *hcBox) TCP(addr string) (net.Conn, error) { return b.cur().TCP(addr) }
func (b *hcBox) UDP() (client.HyUDPConn, error)    { return b.cur().UDP() }
func (b *hcBox) Close() error                      { return b.cur().Close() }
func (b *hcBox) replace(c client.Client) {
	b.mu.Lock()
	old := b.c
	b.c = c
	b.mu.Unlock()
	if old != nil {
		old.Close()
	}
}

// session is one established tunnel.
type session struct {
	server   Server
	link     *hy2Link
	serverIP net.IP
	box      *hcBox
	dev      tun.Device
	nc       *netConfig
	tn       *tunnel
	direct   *directDialer
	stop     chan struct{}
	wg       sync.WaitGroup
}

// Engine owns the tunnel lifecycle. It lives for the whole life of the service;
// Connect/Disconnect create and destroy sessions.
type Engine struct {
	opts     EngineOpts
	dir      string
	store    *serverStore
	router   *router
	procs    *procTable
	logs     *ringLog
	appPaths *appPathCache
	fw       firewallState
	quit     chan struct{}
	bgOnce   sync.Once

	mu     sync.Mutex
	state  string
	errMsg string
	srvID  string
	since  time.Time
	cur    *session
	busy   bool

	lastUp, lastDown int64
	lastAt           time.Time
	upBps, downBps   float64
	speedOnce        sync.Once
}

func newEngine(opts EngineOpts, dir string, secure bool, logs *ringLog) *Engine {
	e := &Engine{
		opts:     opts,
		dir:      dir,
		store:    newServerStore(dir, secure),
		procs:    newProcTable(),
		logs:     logs,
		appPaths: loadAppPathCache(dir),
		quit:     make(chan struct{}),
		state:    stateDisconnected,
		lastAt:   time.Now(),
	}
	// English is the default; Russian only if the user chose it earlier.
	if l := e.store.getLang(); l != "" {
		setLang(l)
	} else {
		setLang("en")
	}
	e.router = newRouter(filepath.Join(dir, "settings.json"), false)
	e.router.load()
	e.router.onChange = func() {
		e.mu.Lock()
		s := e.cur
		e.mu.Unlock()
		if s != nil {
			s.tn.reevaluate()
		}
		go e.applyFirewall()
	}
	return e
}

// Status is a snapshot for the UI.
type Status struct {
	State     string        `json:"state"`
	Error     string        `json:"error,omitempty"`
	ServerID  string        `json:"server_id"`
	Server    string        `json:"server"`
	Uptime    int           `json:"uptime"`
	HasDirect bool          `json:"has_direct"`
	PhysV6    bool          `json:"phys_v6"`
	GeoSize   int           `json:"geo_size"`
	Admin     bool          `json:"admin"`
	TCPFlows  int64         `json:"tcp_flows"`
	TCPFailed int64         `json:"tcp_failed"`
	UDPFlows  int64         `json:"udp_flows"`
	OpenFlows int           `json:"open_flows"`
	ViaTunnel int64         `json:"via_tunnel"`
	ViaDirect int64         `json:"via_direct"`
	BytesUp   int64         `json:"bytes_up"`
	BytesDown int64         `json:"bytes_down"`
	UpBps     float64       `json:"up_bps"`
	DownBps   float64       `json:"down_bps"`
	Recent    []decisionLog `json:"recent"`
}

func (e *Engine) status() Status {
	e.mu.Lock()
	st := Status{State: e.state, Error: e.errMsg, ServerID: e.srvID, Admin: isAdmin()}
	s := e.cur
	since := e.since
	e.mu.Unlock()
	if sv, ok := e.store.get(st.ServerID); ok {
		st.Server = sv.Name
	}
	st.GeoSize = e.router.geo.size()
	st.HasDirect = e.router.hasDirect
	st.PhysV6 = e.router.physV6
	st.ViaTunnel = e.router.tunnelFlow.Load()
	st.ViaDirect = e.router.directFlow.Load()
	st.Recent = e.router.recent()
	if s != nil {
		st.Uptime = int(time.Since(since).Seconds())
		st.TCPFlows, st.TCPFailed, st.UDPFlows = s.tn.tcpFlows.Load(), s.tn.tcpFail.Load(), s.tn.udpFlows.Load()
		st.OpenFlows = s.tn.flows.len()
		st.BytesUp, st.BytesDown = s.tn.bytesUp.Load(), s.tn.bytesDown.Load()
		e.mu.Lock()
		st.UpBps, st.DownBps = e.upBps, e.downBps
		e.mu.Unlock()
	}
	return st
}

// startBackground runs the speed sampler and the firewall loop (once).
func (e *Engine) startBackground() {
	e.bgOnce.Do(func() {
		e.startSpeedSampler()
		go e.firewallLoop()
		go e.applyFirewall()
	})
}

// Shutdown disconnects and removes every firewall rule.
func (e *Engine) Shutdown() {
	e.Disconnect()
	select {
	case <-e.quit:
	default:
		close(e.quit)
	}
	cleanupFirewall()
}

// startSpeedSampler runs once per engine.
func (e *Engine) startSpeedSampler() {
	e.speedOnce.Do(func() {
		go func() {
			for range time.Tick(time.Second) {
				e.mu.Lock()
				s := e.cur
				e.mu.Unlock()
				now := time.Now()
				var up, down int64
				if s != nil {
					up, down = s.tn.bytesUp.Load(), s.tn.bytesDown.Load()
				}
				e.mu.Lock()
				dt := now.Sub(e.lastAt).Seconds()
				if dt > 0 && up >= e.lastUp && down >= e.lastDown {
					e.upBps = float64(up-e.lastUp) / dt
					e.downBps = float64(down-e.lastDown) / dt
				} else {
					e.upBps, e.downBps = 0, 0
				}
				e.lastUp, e.lastDown, e.lastAt = up, down, now
				e.mu.Unlock()
			}
		}()
	})
}

func (e *Engine) setState(state, errMsg string) {
	e.mu.Lock()
	e.state, e.errMsg = state, errMsg
	e.mu.Unlock()
}

// Connect starts a tunnel to the given server (empty id = last used) in the
// background; progress is visible through status().
func (e *Engine) Connect(id string) error {
	srv, ok := e.store.get(id)
	if !ok {
		return errors.New(tr("нет сохранённого сервера: добавьте ссылку hy2://…", "no saved server: add a hy2:// link first"))
	}
	e.mu.Lock()
	if e.busy || e.cur != nil {
		e.mu.Unlock()
		return errors.New(tr("уже подключено или идёт подключение", "already connected or connecting"))
	}
	e.busy = true
	e.state, e.errMsg, e.srvID = stateConnecting, "", srv.ID
	e.mu.Unlock()
	e.store.setLast(srv.ID)

	go func() {
		s, err := e.establish(srv)
		e.mu.Lock()
		e.busy = false
		if err != nil {
			e.state, e.errMsg = stateError, err.Error()
			e.mu.Unlock()
			log.Printf("connect failed: %v", err)
			return
		}
		e.cur = s
		e.state, e.errMsg, e.since = stateConnected, "", time.Now()
		e.mu.Unlock()
		e.applyFirewall()
	}()
	return nil
}

// Disconnect tears the tunnel down and restores the network configuration.
func (e *Engine) Disconnect() {
	e.mu.Lock()
	s := e.cur
	e.cur = nil
	if s == nil && !e.busy {
		e.state, e.errMsg = stateDisconnected, ""
	}
	e.mu.Unlock()
	if s == nil {
		return
	}
	e.teardown(s)
	e.setState(stateDisconnected, "")
	e.applyFirewall()
	log.Printf("disconnected")
}

func (e *Engine) teardown(s *session) {
	close(s.stop)
	s.wg.Wait()
	s.nc.revert()
	s.tn.close()
	s.dev.Close()
	s.box.Close()
}

func resolveServer(host string) (net.IP, error) {
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		return nil, fmt.Errorf(tr("не удалось разрешить %s: %v", "cannot resolve %s: %v"), host, err)
	}
	for _, ip := range ips {
		if ip.To4() != nil {
			return ip, nil
		}
	}
	return ips[0], nil
}

// establish builds a complete session. On any error everything created so far
// is released again.
func (e *Engine) establish(srv Server) (s *session, err error) {
	l, err := parseLink(srv.Link)
	if err != nil {
		return nil, err
	}
	if !isAdmin() {
		return nil, errors.New(tr("нужны права администратора (запустите службу или программу от администратора)", "administrator rights are required (run the service, or run the program as administrator)"))
	}
	serverIP, err := resolveServer(l.Host)
	if err != nil {
		return nil, err
	}
	if l.Obfs != "" || l.Hops() {
		log.Printf("transport: obfs=%q, port hopping=%q", l.Obfs, l.Ports)
	}

	rt, rerr := bestRoute(serverIP)
	if rerr != nil {
		log.Printf("warn: %v", rerr)
	} else {
		log.Printf("server %s via interface %s (%s), gateway %s", serverIP, rt.IfIndex, rt.Desc, rt.NextHop)
		if virtualAdapter.MatchString(rt.Desc) {
			log.Printf("WARNING: the route to the server already goes through a virtual adapter (%s) - another VPN is active", rt.Desc)
		}
	}

	hc, err := newHysteriaClient(l, serverIP, false)
	if err != nil {
		return nil, fmt.Errorf(tr("подключение к серверу: %v", "connecting to the server: %v"), err)
	}
	box := &hcBox{c: hc}
	cleanup := []func(){func() { box.Close() }}
	defer func() {
		if err != nil {
			for i := len(cleanup) - 1; i >= 0; i-- {
				cleanup[i]()
			}
		}
	}()

	tunName := e.opts.TunName
	if runtime.GOOS == "darwin" && (tunName == "" || tunName == "Hy2Tun") {
		tunName = "utun"
	}
	dev, err := tun.CreateTUN(tunName, e.opts.MTU)
	if err != nil {
		return nil, fmt.Errorf(tr("не удалось создать TUN: %v", "cannot create the TUN adapter: %v"), err)
	}
	if realName, rerr := dev.Name(); rerr == nil && realName != "" {
		e.opts.TunName = realName
	}
	cleanup = append(cleanup, func() { dev.Close() })

	var direct *directDialer
	if rt != nil {
		if direct, err = newDirectDialer(rt.IfIndex); err != nil {
			log.Printf("warn: direct path disabled: %v", err)
			err = nil
		}
	}
	e.router.hasDirect = direct != nil
	e.router.physV6 = false
	if rt != nil {
		e.router.physV6 = hasGlobalV6(rt.IfIndex)
		log.Printf("physical adapter IPv6: %v", e.router.physV6)
	}

	tn, err := newTunnel(dev, box, e.router, e.opts.MTU, e.opts.Verbose, e.opts.BlockQUIC)
	if err != nil {
		return nil, err
	}
	tn.direct = direct
	tn.directDNSAddr = e.opts.DirectDNS
	tn.start()
	cleanup = append(cleanup, func() { tn.close() })

	nc := &netConfig{name: e.opts.TunName, serverIP: serverIP, hostGW: rt}
	if err = nc.apply(e.opts.DNS); err != nil {
		nc.revert()
		return nil, fmt.Errorf(tr("настройка сети: %v", "network setup: %v"), err)
	}
	cleanup = append(cleanup, func() { nc.revert() })

	s = &session{server: srv, link: l, serverIP: serverIP, box: box, dev: dev,
		nc: nc, tn: tn, direct: direct, stop: make(chan struct{})}
	s.wg.Add(1)
	go e.watch(s)
	log.Printf("TUN %s is up: traffic goes through %s", e.opts.TunName, l.hostPort())
	return s, nil
}

// watch keeps the tunnel alive across network changes and sleep/resume.
func (e *Engine) watch(s *session) {
	defer s.wg.Done()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	last := time.Now()
	n := 0
	for {
		select {
		case <-s.stop:
			return
		case now := <-tick.C:
			resumed := now.Sub(last) > 15*time.Second // the PC was asleep
			last = now
			n++
			cur, _ := parseIfIndex(ifIndexOf(s))
			idx, err := bestInterfaceFor(s.serverIP)
			changed := err != nil || idx != cur
			if !resumed && !changed && n%15 != 0 {
				continue // healthy; re-check the gateway every ~30 s
			}
			rt, err := physicalDefaultRoute(e.opts.TunName)
			if err != nil {
				if changed || resumed {
					log.Printf("network: %v", err)
				}
				continue
			}
			same := s.nc.hostGW != nil && s.nc.hostGW.IfIndex == rt.IfIndex && s.nc.hostGW.NextHop == rt.NextHop
			if same && !resumed {
				continue
			}
			e.rebind(s, rt, resumed, same)
		}
	}
}

func ifIndexOf(s *session) string {
	if s.nc.hostGW == nil {
		return "0"
	}
	return s.nc.hostGW.IfIndex
}

// rebind adapts the session to a new physical route (or to a resume from sleep).
func (e *Engine) rebind(s *session, rt *physRoute, resumed, sameRoute bool) {
	if resumed {
		log.Printf("network: resumed from sleep, refreshing the connection")
	}
	if !sameRoute {
		log.Printf("network: switching to interface %s (%s), gateway %s", rt.IfIndex, rt.Desc, rt.NextHop)
		if err := s.nc.rebindHost(rt); err != nil {
			log.Printf("network: %v", err)
		}
		if idx, err := parseIfIndex(rt.IfIndex); err == nil {
			if s.direct != nil {
				s.direct.setIfIndex(idx)
			}
			e.router.hasDirect = s.direct != nil
		}
		e.router.physV6 = hasGlobalV6(rt.IfIndex)
	}
	// The QUIC connection is bound to the old path: start a fresh client.
	if nc, err := newHysteriaClient(s.link, s.serverIP, true); err == nil {
		s.box.replace(nc)
	} else {
		log.Printf("network: client refresh failed: %v", err)
	}
}

// ringLog keeps the most recent log lines for the UI.
type ringLog struct {
	mu    sync.Mutex
	lines []string
	cap   int
	part  string
}

func newRingLog(cap int) *ringLog { return &ringLog{cap: cap} }

func (r *ringLog) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	data := r.part + string(p)
	parts := strings.Split(data, "\n")
	r.part = parts[len(parts)-1]
	for _, l := range parts[:len(parts)-1] {
		if l = strings.TrimRight(l, "\r"); l != "" {
			r.lines = append(r.lines, l)
		}
	}
	if len(r.lines) > r.cap {
		r.lines = r.lines[len(r.lines)-r.cap:]
	}
	return len(p), nil
}

func (r *ringLog) tail(n int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n > len(r.lines) {
		n = len(r.lines)
	}
	return append([]string(nil), r.lines[len(r.lines)-n:]...)
}

var _ atomic.Int32
