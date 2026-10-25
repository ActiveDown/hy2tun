//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c -fmodules
#cgo LDFLAGS: -framework Cocoa -framework WebKit

#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>

@interface Hy2TunWinDelegate : NSObject <NSWindowDelegate>
@end

@implementation Hy2TunWinDelegate
- (BOOL)windowShouldClose:(NSWindow *)sender {
    [sender orderOut:nil];
    return NO;
}
@end

static NSWindow *globalWindow = nil;
static WKWebView *globalWebView = nil;
static Hy2TunWinDelegate *globalDelegate = nil;

void setupMacAppMenu(const void *iconBytes, int iconLen) {
    dispatch_async(dispatch_get_main_queue(), ^{
        [NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];

        if (iconBytes != NULL && iconLen > 0) {
            NSData *data = [NSData dataWithBytes:iconBytes length:iconLen];
            NSImage *img = [[NSImage alloc] initWithData:data];
            if (img != nil) {
                [NSApp setApplicationIconImage:img];
            }
        }

        NSMenu *mainMenu = [[NSMenu alloc] init];
        NSMenuItem *appMenuItem = [[NSMenuItem alloc] init];
        [mainMenu addItem:appMenuItem];

        NSMenuItem *editMenuItem = [[NSMenuItem alloc] init];
        [mainMenu addItem:editMenuItem];

        NSMenu *editMenu = [[NSMenu alloc] initWithTitle:@"Edit"];
        [editMenu addItemWithTitle:@"Undo" action:@selector(undo:) keyEquivalent:@"z"];
        [editMenu addItemWithTitle:@"Redo" action:@selector(redo:) keyEquivalent:@"Z"];
        [editMenu addItem:[NSMenuItem separatorItem]];
        [editMenu addItemWithTitle:@"Cut" action:@selector(cut:) keyEquivalent:@"x"];
        [editMenu addItemWithTitle:@"Copy" action:@selector(copy:) keyEquivalent:@"c"];
        [editMenu addItemWithTitle:@"Paste" action:@selector(paste:) keyEquivalent:@"v"];
        [editMenu addItemWithTitle:@"Select All" action:@selector(selectAll:) keyEquivalent:@"a"];
        [editMenuItem setSubmenu:editMenu];

        [NSApp setMainMenu:mainMenu];
    });
}

void showNativeWindow(const char *urlStr) {
    dispatch_async(dispatch_get_main_queue(), ^{
        if (globalWindow == nil) {
            NSRect frame = NSMakeRect(0, 0, 460, 800);
            NSWindowStyleMask mask = NSWindowStyleMaskTitled | NSWindowStyleMaskClosable | NSWindowStyleMaskMiniaturizable;
            globalWindow = [[NSWindow alloc] initWithContentRect:frame
                                                       styleMask:mask
                                                         backing:NSBackingStoreBuffered
                                                           defer:NO];
            [globalWindow setTitle:@"Hy2Tun"];
            [globalWindow center];
            [globalWindow setAppearance:[NSAppearance appearanceNamed:NSAppearanceNameDarkAqua]];
            [globalWindow setBackgroundColor:[NSColor colorWithRed:14.0/255.0 green:16.0/255.0 blue:21.0/255.0 alpha:1.0]];

            globalDelegate = [[Hy2TunWinDelegate alloc] init];
            [globalWindow setDelegate:globalDelegate];

            WKWebViewConfiguration *config = [[WKWebViewConfiguration alloc] init];
            globalWebView = [[WKWebView alloc] initWithFrame:[[globalWindow contentView] bounds] configuration:config];
            [globalWebView setAutoresizingMask:(NSViewWidthSizable | NSViewHeightSizable)];
            [globalWebView setValue:@NO forKey:@"drawsBackground"];
            [[globalWindow contentView] addSubview:globalWebView];
        }

        if (urlStr != NULL && strlen(urlStr) > 0) {
            NSString *nsUrl = [NSString stringWithUTF8String:urlStr];
            NSURLRequest *req = [NSURLRequest requestWithURL:[NSURL URLWithString:nsUrl]];
            [globalWebView loadRequest:req];
        }

        [globalWindow makeKeyAndOrderFront:nil];
        [NSApp activateIgnoringOtherApps:YES];
    });
}

void loadNativeHTML(const char *htmlStr) {
    dispatch_async(dispatch_get_main_queue(), ^{
        if (globalWebView != nil && htmlStr != NULL) {
            NSString *nsHtml = [NSString stringWithUTF8String:htmlStr];
            [globalWebView loadHTMLString:nsHtml baseURL:nil];
        }
    });
}
*/
import "C"

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image/color"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

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
	title := tr("Служба Hy2Tun запускается...", "Hy2Tun service is starting...")
	body := tr("Если служба ещё не запущена, панель подключится автоматически через пару секунд.",
		"If the service is not running yet, the panel will connect automatically in a few seconds.")
	return `<!doctype html><meta charset="utf-8"><body style="margin:0;height:100vh;display:grid;place-items:center;background:#0e1015;color:#eceff5;font:15px -apple-system,BlinkMacSystemFont,sans-serif;text-align:center">
<div><div style="width:40px;height:40px;border-radius:12px;margin:0 auto 14px;background:linear-gradient(135deg,#7a5cff,#00c2ff)"></div>
<b>` + title + `</b><p style="color:#8b93a5;max-width:340px;line-height:1.4">` + body + `</p></div></body>`
}

func (g *gui) openWindow() {
	g.mu.Lock()
	g.windowOn = true
	g.mu.Unlock()

	if g.serviceUp() {
		cURL := C.CString(g.pageURL())
		defer C.free(unsafe.Pointer(cURL))
		C.showNativeWindow(cURL)
	} else {
		cEmpty := C.CString("")
		defer C.free(unsafe.Pointer(cEmpty))
		C.showNativeWindow(cEmpty)

		cHTML := C.CString(waitingHTML())
		defer C.free(unsafe.Pointer(cHTML))
		C.loadNativeHTML(cHTML)

		go func() {
			for i := 0; i < 600; i++ {
				time.Sleep(500 * time.Millisecond)
				if g.serviceUp() {
					cURL := C.CString(g.pageURL())
					C.showNativeWindow(cURL)
					C.free(unsafe.Pointer(cURL))
					return
				}
			}
		}()
	}
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
	// Setup App Icon and Cocoa Edit menu
	appIconPNG := logo.PNG(logo.App(128))
	C.setupMacAppMenu(unsafe.Pointer(&appIconPNG[0]), C.int(len(appIconPNG)))

	systray.SetIcon(iconOff)
	systray.SetTitle("Hy2Tun")
	systray.SetTooltip("Hy2Tun — " + tr("отключено", "disconnected"))

	open := systray.AddMenuItem(tr("Открыть", "Open"), tr("Показать окно", "Show the window"))
	g.openMI = open
	g.connectMI = systray.AddMenuItem(tr("Подключить", "Connect"), "")
	systray.AddSeparator()
	quit := systray.AddMenuItem(tr("Выход", "Exit"), tr("Закрыть Hy2Tun", "Close Hy2Tun"))
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
				os.Exit(0)
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
	g.quitMI.SetTooltip(tr("Закрыть Hy2Tun", "Close Hy2Tun"))
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
	dir, portable := resolveDataDir(c.data)
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

	// If background service is not running, start an in-process engine API so the window always works!
	if !g.serviceUp() {
		ring, _ := setupLogging(dir, false)
		e := newEngine(c.engineOpts(), dir, !portable, ring)
		importLink(e, c.link)
		migrateLegacyLink(e)
		api, _, err := startAPI(e, c.ui, !portable)
		if err == nil {
			defer api.close()
			log.Printf("started local Hy2Tun engine at %s", c.ui)
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
