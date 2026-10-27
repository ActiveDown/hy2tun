package main

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The Android app drives the engine through a single JNI entry point
// (Core.nativeCall, see jni_android.go): a method name and a JSON argument in,
// {"ok":..,"error":..,"data":..} out. The window itself is the desktop panel
// (ui.html) served by the same local API as on Windows and macOS.

var mob struct {
	mu   sync.Mutex
	e    *Engine
	ring *ringLog
	ui   string // panel URL with the token
	gen  int    // bumped by every connect / disconnect request
}

type mobReply struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Data  any    `json:"data,omitempty"`
}

func mobileCall(method string, arg []byte) []byte {
	data, err := mobileDispatch(method, arg)
	r := mobReply{OK: err == nil, Data: data}
	if err != nil {
		r.Error = err.Error()
	}
	b, _ := json.Marshal(r)
	return b
}

// mobileInit creates the engine and the local API once; dir is the app's files
// directory.
func mobileInit(dir, lang string) error {
	mob.mu.Lock()
	defer mob.mu.Unlock()
	if mob.e != nil {
		return nil
	}
	if dir == "" {
		return errors.New("no data directory")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	ring := newRingLog(500)
	w := io.MultiWriter(ring, logcatWriter{})
	if f, err := openRotating(filepath.Join(dir, "hy2tun.log")); err == nil {
		w = io.MultiWriter(ring, f, logcatWriter{})
	}
	log.SetOutput(w)
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)

	e := newEngine(defaultEngineOpts(), dir, true, ring)
	if e.store.getLang() == "" {
		setLang(lang) // until the user picks one, follow the system language
	}
	// a random port: only this app knows it (and the token)
	_, url, err := startAPI(e, "127.0.0.1:0", true)
	if err != nil {
		return err
	}
	mob.e, mob.ring, mob.ui = e, ring, url
	log.Printf("hy2tun %s (android) started", appVersion)
	return nil
}

func mobEngine() *Engine {
	mob.mu.Lock()
	defer mob.mu.Unlock()
	return mob.e
}

func bumpGen() int {
	mob.mu.Lock()
	defer mob.mu.Unlock()
	mob.gen++
	return mob.gen
}

// finishDisconnect: Engine.Disconnect cannot stop a connection that is still
// being set up, and a session coming up after the user pressed Disconnect would
// keep the VPN alive. Wait for the attempt and tear it down, unless a new
// connect was requested meanwhile.
func finishDisconnect(e *Engine, gen int) {
	for i := 0; i < 600 && e.status().State == stateConnecting; i++ {
		time.Sleep(100 * time.Millisecond)
	}
	mob.mu.Lock()
	same := mob.gen == gen
	mob.mu.Unlock()
	if same {
		e.Disconnect()
	}
}

func mobileDispatch(method string, arg []byte) (any, error) {
	var a struct {
		Dir  string         `json:"dir"`
		Lang string         `json:"lang"`
		ID   string         `json:"id"`
		Link string         `json:"link"`
		Name string         `json:"name"`
		DNS  []string       `json:"dns"`
		V6   bool           `json:"v6"`
		Apps []installedApp `json:"apps"`
	}
	if len(arg) > 0 {
		if err := json.Unmarshal(arg, &a); err != nil {
			return nil, err
		}
	}
	switch method {
	case "init":
		return nil, mobileInit(a.Dir, a.Lang)
	case "version":
		return appVersion, nil
	}
	e := mobEngine()
	if e == nil {
		return nil, errors.New("engine is not initialised")
	}
	switch method {
	case "ui":
		return mob.ui, nil
	case "status":
		return e.status(), nil
	case "config":
		return e.router.config(), nil
	case "tun":
		return map[string]any{
			"addr4": tunV4Addr, "prefix4": tunV4Prefix,
			"addr6": tunV6Addr, "prefix6": tunV6Prefix,
			"mtu": e.opts.MTU, "dns": e.opts.DNS,
		}, nil
	case "addServer":
		srv, err := e.store.add(a.Link, a.Name)
		if err != nil {
			return nil, err
		}
		return map[string]string{"id": srv.ID, "name": srv.Name}, nil
	case "network":
		setPhysNet(a.Name, a.DNS, a.V6)
		return nil, nil
	case "apps":
		setInstalledApps(a.Apps)
		return nil, nil
	case "connect":
		// the always-on VPN started by the system: the last used profile
		bumpGen()
		return nil, e.Connect(a.ID)
	case "reconnect":
		// the per-app list is part of the VPN interface: rebuild it
		id := e.status().ServerID
		bumpGen()
		e.Disconnect()
		return nil, e.Connect(id)
	case "disconnect":
		gen := bumpGen()
		e.Disconnect()
		if e.status().State == stateConnecting {
			go finishDisconnect(e, gen)
		}
		return nil, nil
	}
	return nil, errors.New("unknown method " + method)
}
