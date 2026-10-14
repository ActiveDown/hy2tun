package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"fyne.io/systray"
	"github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

const (
	guiWakeAddr = "127.0.0.1:7879" // second launches poke the running GUI here
	guiMutex    = `Local\Hy2TunGUI`
)

// gui is the desktop shell: a tray icon plus a native window that hosts the UI
// served by the service.
type gui struct {
	dir     string
	apiAddr string

	mu        sync.Mutex
	windowOn  bool
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

func waitingHTML() string {
	title := tr("Служба Hy2Tun не отвечает", "The Hy2Tun service is not responding")
	body := tr("Она запускается вместе с Windows. Если вы только что установили программу, подождите несколько секунд — окно откроется само.",
		"It starts together with Windows. If you have just installed the program, wait a few seconds — the window will open by itself.")
	return `<!doctype html><meta charset="utf-8"><body style="margin:0;height:100vh;display:grid;place-items:center;background:#0e1015;color:#eceff5;font:15px 'Segoe UI',sans-serif;text-align:center">
<div><div style="width:40px;height:40px;border-radius:12px;margin:0 auto 14px;background:linear-gradient(135deg,#7a5cff,#00c2ff)"></div>
<b>` + title + `</b><p style="color:#8b93a5;max-width:340px">` + body + `</p></div></body>`
}

// openWindow shows the UI window; it is a no-op while one is already open.
func (g *gui) openWindow() {
	g.mu.Lock()
	if g.windowOn {
		g.mu.Unlock()
		return
	}
	g.windowOn = true
	g.mu.Unlock()

	go func() {
		runtime.LockOSThread()
		defer func() {
			g.mu.Lock()
			g.windowOn = false
			g.mu.Unlock()
		}()
		data := filepath.Join(os.Getenv("LOCALAPPDATA"), "Hy2Tun", "webview2")
		os.MkdirAll(data, 0o755)
		w := webview2.NewWithOptions(webview2.WebViewOptions{
			DataPath:  data,
			AutoFocus: true,
			WindowOptions: webview2.WindowOptions{
				Title: "Hy2Tun", Width: 460, Height: 800, Center: true,
			},
		})
		if w == nil {
			msgBox("Hy2Tun", tr("Не удалось открыть окно: нужен Microsoft Edge WebView2 Runtime.", "Cannot open the window: Microsoft Edge WebView2 Runtime is required.")+
				"\nhttps://developer.microsoft.com/microsoft-edge/webview2/")
			return
		}
		defer w.Destroy()
		w.SetSize(460, 800, webview2.HintNone)
		if g.serviceUp() {
			w.Navigate(g.pageURL())
		} else {
			w.SetHtml(waitingHTML())
			go func() { // navigate as soon as the service answers
				for i := 0; i < 600; i++ {
					time.Sleep(time.Second)
					if g.serviceUp() {
						w.Dispatch(func() { w.Navigate(g.pageURL()) })
						return
					}
				}
			}()
		}
		w.Run()
	}()
}

func msgBox(title, text string) {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(text)
	windows.MessageBox(0, m, t, windows.MB_OK|windows.MB_ICONINFORMATION)
}

// ---- tray icon ----

func trayIcon(c color.RGBA) []byte {
	const n = 32
	img := image.NewRGBA(image.Rect(0, 0, n, n))
	cx, cy, r := float64(n)/2-0.5, float64(n)/2-0.5, float64(n)/2-2
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			d := dx*dx + dy*dy
			switch {
			case d <= (r-4)*(r-4):
				img.SetRGBA(x, y, c)
			case d <= r*r:
				img.SetRGBA(x, y, color.RGBA{c.R / 2, c.G / 2, c.B / 2, 255})
			}
		}
	}
	var pngBuf bytes.Buffer
	png.Encode(&pngBuf, img)
	// ICO container with one PNG image
	var ico bytes.Buffer
	binary.Write(&ico, binary.LittleEndian, []uint16{0, 1, 1})
	ico.Write([]byte{n, n, 0, 0})
	binary.Write(&ico, binary.LittleEndian, []uint16{1, 32})
	binary.Write(&ico, binary.LittleEndian, uint32(pngBuf.Len()))
	binary.Write(&ico, binary.LittleEndian, uint32(22))
	ico.Write(pngBuf.Bytes())
	return ico.Bytes()
}

var (
	iconOff  = trayIcon(color.RGBA{140, 148, 165, 255})
	iconOn   = trayIcon(color.RGBA{61, 220, 132, 255})
	iconBusy = trayIcon(color.RGBA{240, 178, 74, 255})
	iconErr  = trayIcon(color.RGBA{239, 83, 80, 255})
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

// applyTray sets icon, tooltip and menu texts for a state in the current language.
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

// poll mirrors the service state (and the language) into the tray icon and menu.
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
				state, name, svcLang = d.Status.State, d.Status.Server, d.Lang
			}
		}
		langChanged := false
		if l := normalizeLang(svcLang); l != "" && l != lang() {
			setLang(l)
			langChanged = true
		}
		g.mu.Lock()
		changed := state != g.lastState || langChanged
		g.lastState = state
		g.mu.Unlock()
		if changed {
			g.applyTray(state, name)
		}
		time.Sleep(2 * time.Second)
	}
}

// cmdGUI starts the tray + window. A second launch only wakes the first one.
func cmdGUI(args []string) {
	c, _ := parseFlags(args)
	tray := false
	for _, a := range args {
		if a == "--tray" || a == "-tray" {
			tray = true
		}
	}
	dir, _ := resolveDataDir(c.data)

	// single instance
	name, _ := windows.UTF16PtrFromString(guiMutex)
	h, err := windows.CreateMutex(nil, false, name)
	if err == windows.ERROR_ALREADY_EXISTS {
		if conn, err := net.DialTimeout("tcp", guiWakeAddr, time.Second); err == nil {
			conn.Write([]byte("show\n"))
			conn.Close()
		}
		return
	}
	_ = h

	g := &gui{dir: dir, apiAddr: c.ui}
	if ln, err := net.Listen("tcp", guiWakeAddr); err == nil {
		go func() {
			for {
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				conn.Close()
				g.openWindow()
			}
		}()
	}
	if !tray {
		go func() { time.Sleep(300 * time.Millisecond); g.openWindow() }()
	}
	systray.Run(g.onReady, func() {})
}
