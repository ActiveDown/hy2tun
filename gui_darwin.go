//go:build darwin

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image/color"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"fyne.io/systray"
	"github.com/ActiveDown/hy2tun/internal/logo"
)

const (
	guiWakeAddr = "127.0.0.1:7879" // second launches poke the running GUI here
)

type gui struct {
	dir     string
	apiAddr string

	mu        sync.Mutex
	connectMI *systray.MenuItem
	openMI    *systray.MenuItem
	quitMI    *systray.MenuItem
	lastState string
}

func (g *gui) token() string {
	b, err := os.ReadFile(filepath.Join(g.dir, "token"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (g *gui) call(method, path string, body any) ([]byte, error) {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, "http://"+g.apiAddr+path, r)
	req.Header.Set("X-Token", g.token())
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return b, fmt.Errorf("%s", strings.TrimSpace(string(b)))
	}
	return b, nil
}

func (g *gui) serviceUp() bool {
	_, err := g.call("GET", "/api/state", nil)
	return err == nil
}

func (g *gui) pageURL() string { return "http://" + g.apiAddr + "/?t=" + g.token() }

func (g *gui) openWindow() {
	_ = exec.Command("open", g.pageURL()).Start()
}

func msgBox(title, text string) {
	script := fmt.Sprintf(`display dialog %q with title %q buttons {"OK"} default button "OK"`, text, title)
	_ = exec.Command("osascript", "-e", script).Run()
}

// ---- tray icon ----

func trayIcon(dot *color.NRGBA, gray bool) []byte {
	im := logo.App(32)
	if gray {
		im = logo.Gray(im)
	}
	if dot != nil {
		im = logo.Dot(im, *dot)
	}
	return logo.PNG(im)
}

var (
	iconOff  = trayIcon(nil, true)
	iconOn   = trayIcon(&color.NRGBA{61, 220, 132, 255}, false)
	iconBusy = trayIcon(&color.NRGBA{240, 178, 74, 255}, false)
	iconErr  = trayIcon(&color.NRGBA{239, 83, 80, 255}, false)
)

func (g *gui) onReady() {
	systray.SetIcon(iconOff)
	systray.SetTitle("Hy2Tun")
	systray.SetTooltip("Hy2Tun — " + tr("отключено", "disconnected"))

	open := systray.AddMenuItem(tr("Открыть", "Open"), tr("Показать окно", "Show the window"))
	g.openMI = open
	g.connectMI = systray.AddMenuItem(tr("Подключить", "Connect"), "")
	systray.AddSeparator()
	quit := systray.AddMenuItem(tr("Выход", "Exit"), tr("Закрыть значок (служба продолжит работать)", "Close the tray icon (the service keeps running)"))
	g.quitMI = quit
	systray.SetOnTapped(g.openWindow)

	go func() {
		for {
			select {
			case <-open.ClickedCh:
				g.openWindow()
			case <-g.connectMI.ClickedCh:
				g.toggle()
			case <-quit.ClickedCh:
				systray.Quit()
				return
			}
		}
	}()
	go g.poll()
}

func (g *gui) toggle() {
	g.mu.Lock()
	st := g.lastState
	g.mu.Unlock()
	if st == stateConnected || st == stateConnecting {
		g.call("POST", "/api/disconnect", struct{}{})
	} else if _, err := g.call("POST", "/api/connect", map[string]string{"id": ""}); err != nil {
		msgBox("Hy2Tun", err.Error())
	}
}

func (g *gui) applyTray(state, name string) {
	g.openMI.SetTitle(tr("Открыть", "Open"))
	g.openMI.SetTooltip(tr("Показать окно", "Show the window"))
	g.quitMI.SetTitle(tr("Выход", "Exit"))
	g.quitMI.SetTooltip(tr("Закрыть значок (служба продолжит работать)", "Close the tray icon (the service keeps running)"))
	switch state {
	case stateConnected:
		systray.SetIcon(iconOn)
		systray.SetTooltip("Hy2Tun — " + tr("подключено: ", "connected: ") + name)
		g.connectMI.SetTitle(tr("Отключить", "Disconnect"))
		g.connectMI.Enable()
	case stateConnecting:
		systray.SetIcon(iconBusy)
		systray.SetTooltip("Hy2Tun — " + tr("подключение…", "connecting…"))
		g.connectMI.SetTitle(tr("Отменить", "Cancel"))
		g.connectMI.Enable()
	case stateError:
		systray.SetIcon(iconErr)
		systray.SetTooltip("Hy2Tun — " + tr("ошибка", "error"))
		g.connectMI.SetTitle(tr("Подключить", "Connect"))
		g.connectMI.Enable()
	case "unreachable":
		systray.SetIcon(iconOff)
		systray.SetTooltip("Hy2Tun — " + tr("служба недоступна", "service unavailable"))
		g.connectMI.SetTitle(tr("Подключить", "Connect"))
		g.connectMI.Disable()
	default:
		systray.SetIcon(iconOff)
		systray.SetTooltip("Hy2Tun — " + tr("отключено", "disconnected"))
		g.connectMI.SetTitle(tr("Подключить", "Connect"))
		g.connectMI.Enable()
	}
}

func (g *gui) poll() {
	for {
		state, name := "unreachable", ""
		svcLang := ""
		if b, err := g.call("GET", "/api/state", nil); err == nil {
			var d struct {
				Status Status `json:"status"`
				Lang   string `json:"lang"`
			}
			if json.Unmarshal(b, &d) == nil {
				state = d.Status.State
				name = d.Status.Server
				svcLang = d.Lang
			}
		}
		if svcLang != "" && svcLang != lang() {
			setLang(svcLang)
		}
		g.mu.Lock()
		prev := g.lastState
		g.lastState = state
		g.mu.Unlock()
		if state != prev {
			g.applyTray(state, name)
		}
		time.Sleep(time.Second)
	}
}

func (g *gui) onExit() {}

func cmdGUI(args []string) {
	c, _ := parseFlags(args)
	dir, _ := resolveDataDir(c.data)
	g := &gui{dir: dir, apiAddr: c.ui}

	// Single instance wake mechanism
	if ln, err := net.Listen("tcp", guiWakeAddr); err == nil {
		go func() {
			defer ln.Close()
			for {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				conn.Close()
				g.openWindow()
			}
		}()
	} else {
		if conn, err := net.DialTimeout("tcp", guiWakeAddr, 500*time.Millisecond); err == nil {
			conn.Close()
			return
		}
	}

	trayOnly := false
	for _, a := range os.Args[1:] {
		if a == "--tray" || a == "-tray" {
			trayOnly = true
			break
		}
	}

	if !trayOnly {
		go func() {
			time.Sleep(300 * time.Millisecond)
			g.openWindow()
		}()
	}

	systray.Run(g.onReady, g.onExit)
}
