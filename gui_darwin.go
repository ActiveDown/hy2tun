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

static NSURL *globalURL = nil;

@interface Hy2TunNavDelegate : NSObject <WKNavigationDelegate, WKUIDelegate>
@end

@implementation Hy2TunNavDelegate
- (void)webView:(WKWebView *)webView didStartProvisionalNavigation:(WKNavigation *)navigation {
    NSLog(@"[Hy2Tun WebKit] didStartProvisionalNavigation: %@", webView.URL);
}
- (void)webView:(WKWebView *)webView didFailProvisionalNavigation:(WKNavigation *)navigation withError:(NSError *)error {
    NSLog(@"[Hy2Tun WebKit] didFailProvisionalNavigation error: %@", error);
    // The service may be restarting: keep trying the latest address until it answers.
    dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(1.0 * NSEC_PER_SEC)), dispatch_get_main_queue(), ^{
        if (globalURL != nil && webView.URL == nil) {
            [webView loadRequest:[NSURLRequest requestWithURL:globalURL]];
        }
    });
}
// WKWebView ignores alert()/confirm() unless the host implements these.
- (void)webView:(WKWebView *)webView runJavaScriptAlertPanelWithMessage:(NSString *)message initiatedByFrame:(WKFrameInfo *)frame completionHandler:(void (^)(void))completionHandler {
    NSAlert *a = [[NSAlert alloc] init];
    [a setMessageText:message];
    [a addButtonWithTitle:@"OK"];
    [a runModal];
    completionHandler();
}
- (void)webView:(WKWebView *)webView runJavaScriptConfirmPanelWithMessage:(NSString *)message initiatedByFrame:(WKFrameInfo *)frame completionHandler:(void (^)(BOOL result))completionHandler {
    NSAlert *a = [[NSAlert alloc] init];
    [a setMessageText:message];
    [a addButtonWithTitle:@"OK"];
    [a addButtonWithTitle:@"Cancel"];
    completionHandler([a runModal] == NSAlertFirstButtonReturn);
}
- (void)webView:(WKWebView *)webView didFinishNavigation:(WKNavigation *)navigation {
    NSLog(@"[Hy2Tun WebKit] didFinishNavigation: %@", webView.URL);
}
- (void)webView:(WKWebView *)webView didFailNavigation:(WKNavigation *)navigation withError:(NSError *)error {
    NSLog(@"[Hy2Tun WebKit] didFailNavigation error: %@", error);
}
@end

static NSWindow *globalWindow = nil;
static WKWebView *globalWebView = nil;
static Hy2TunWinDelegate *globalWinDelegate = nil;
static Hy2TunNavDelegate *globalNavDelegate = nil;

void setupMacAppMenu(const void *iconBytes, int iconLen) {
    NSData *data = (iconBytes != NULL && iconLen > 0) ? [NSData dataWithBytes:iconBytes length:iconLen] : nil;
    dispatch_async(dispatch_get_main_queue(), ^{
        [NSApp setActivationPolicy:NSApplicationActivationPolicyRegular];

        if (data != nil) {
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
    NSString *nsUrl = (urlStr != NULL && strlen(urlStr) > 0) ? [NSString stringWithUTF8String:urlStr] : nil;
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

            globalWinDelegate = [[Hy2TunWinDelegate alloc] init];
            [globalWindow setDelegate:globalWinDelegate];

            WKWebViewConfiguration *config = [[WKWebViewConfiguration alloc] init];
            #if __MAC_OS_X_VERSION_MAX_ALLOWED >= 130300
            if (@available(macOS 13.3, *)) {
                [config.preferences setValue:@YES forKey:@"developerExtrasEnabled"];
            }
            #endif

            globalWebView = [[WKWebView alloc] initWithFrame:[[globalWindow contentView] bounds] configuration:config];
            [globalWebView setAutoresizingMask:(NSViewWidthSizable | NSViewHeightSizable)];
            [globalWebView setValue:@NO forKey:@"drawsBackground"];

            globalNavDelegate = [[Hy2TunNavDelegate alloc] init];
            [globalWebView setNavigationDelegate:globalNavDelegate];
            [globalWebView setUIDelegate:globalNavDelegate];

            [[globalWindow contentView] addSubview:globalWebView];
        }

        if (nsUrl != nil && [nsUrl length] > 0) {
            NSURL *url = [NSURL URLWithString:nsUrl];
            if (url != nil) {
                globalURL = url;
                NSURLRequest *req = [NSURLRequest requestWithURL:url];
                [globalWebView loadRequest:req];
            }
        }

        [globalWindow makeKeyAndOrderFront:nil];
        [NSApp activateIgnoringOtherApps:YES];
    });
}

// Points an existing window at a new address (the service was installed or restarted
// and has a new token) without bringing it to the front.
void reloadNativeURL(const char *urlStr) {
    NSString *nsUrl = (urlStr != NULL) ? [NSString stringWithUTF8String:urlStr] : nil;
    dispatch_async(dispatch_get_main_queue(), ^{
        if (globalWebView == nil || nsUrl == nil) {
            return;
        }
        NSURL *url = [NSURL URLWithString:nsUrl];
        if (url != nil) {
            globalURL = url;
            [globalWebView loadRequest:[NSURLRequest requestWithURL:url]];
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
	guiWakeAddr  = "127.0.0.1:7879" // second launches poke the running GUI here
	guiLocalAddr = "127.0.0.1:7880" // in-process engine while the root service is not installed
)

func apiCall(addr, token, method, path string, body any) ([]byte, error) {
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, "http://"+addr+path, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Token", token)
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

// gui is the tray + window process. The window talks to the root service when it
// runs; otherwise to an engine inside this process (it can only ask for the
// service to be installed). The poller switches between the two on its own.
type gui struct {
	svcAddr  string
	svcDir   string // extra place to look for the service token (portable / -data)
	localDir string
	portable bool
	opts     EngineOpts
	link     string

	upgradeAsked bool

	mu          sync.Mutex
	local       bool // the window currently uses the in-process engine
	svcTok      string
	localEngine *Engine
	localAPI    *apiServer
	windowOn    bool
	loadedURL   string
	connectMI   *systray.MenuItem
	openMI      *systray.MenuItem
	quitMI      *systray.MenuItem
	lastState   string
}

// findService returns the control token that the running root service accepts
// ("" when it does not answer). Both the system data dir and a custom one are tried.
func (g *gui) findService() string {
	for _, dir := range []string{systemDataDir, g.svcDir} {
		if tok := readTokenFile(dir); tok != "" {
			if _, err := apiCall(g.svcAddr, tok, "GET", "/api/state", nil); err == nil {
				return tok
			}
		}
	}
	return ""
}

func (g *gui) endpoint() (addr, token string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.local && g.localAPI != nil {
		return guiLocalAddr, g.localAPI.token
	}
	return g.svcAddr, g.svcTok
}

func (g *gui) call(method, path string, body any) ([]byte, error) {
	addr, tok := g.endpoint()
	return apiCall(addr, tok, method, path, body)
}

func (g *gui) pageURL() string {
	addr, tok := g.endpoint()
	return "http://" + addr + "/?t=" + tok
}

func (g *gui) startLocal() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.localAPI != nil {
		return true
	}
	if g.localEngine == nil {
		ring, _ := setupLogging(g.localDir, false)
		g.localEngine = newEngine(g.opts, g.localDir, !g.portable, ring)
		importLink(g.localEngine, g.link)
		migrateLegacyLink(g.localEngine)
	}
	api, _, err := startAPI(g.localEngine, guiLocalAddr, !g.portable)
	if err != nil {
		log.Printf("local engine: %v", err)
		return false
	}
	g.localAPI = api
	log.Printf("started local Hy2Tun engine at %s", guiLocalAddr)
	return true
}

func (g *gui) stopLocal() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.localAPI != nil {
		g.localAPI.close()
		g.localAPI = nil
		log.Printf("local engine stopped: the root service answers now")
	}
}

// ensureBackend picks the API the window should use: the root service when it
// answers, otherwise the in-process engine (started on demand).
func (g *gui) ensureBackend() {
	if tok := g.findService(); tok != "" {
		g.stopLocal()
		g.mu.Lock()
		g.local, g.svcTok = false, tok
		g.mu.Unlock()
		return
	}
	if g.startLocal() {
		g.mu.Lock()
		g.local = true
		g.mu.Unlock()
	}
}

// syncWindow reloads the window when the address or token it should use changed
// (service installed, restarted or stopped).
func (g *gui) syncWindow() {
	g.mu.Lock()
	on, loaded := g.windowOn, g.loadedURL
	g.mu.Unlock()
	if !on {
		return
	}
	if url := g.pageURL(); url != loaded {
		g.loadURL(url, false)
	}
}

func (g *gui) loadURL(url string, show bool) {
	g.mu.Lock()
	g.loadedURL = url
	g.mu.Unlock()
	cURL := C.CString(url)
	defer C.free(unsafe.Pointer(cURL))
	if show {
		C.showNativeWindow(cURL)
	} else {
		C.reloadNativeURL(cURL)
	}
}

func (g *gui) openWindow() {
	g.mu.Lock()
	g.windowOn = true
	g.mu.Unlock()
	g.loadURL(g.pageURL(), true)
}

func msgBox(title, text string) {
	script := fmt.Sprintf(`display dialog %q with title %q buttons {"OK"} default button "OK"`, text, title)
	_ = exec.Command("osascript", "-e", script).Run()
}

// confirmBox asks a yes/no question with a native dialog.
func confirmBox(title, text string) bool {
	script := fmt.Sprintf(`display dialog %q with title %q buttons {"%s", "%s"} default button "%s" cancel button "%s"`,
		text, title, tr("Позже", "Later"), tr("Обновить", "Update"), tr("Обновить", "Update"), tr("Позже", "Later"))
	return exec.Command("osascript", "-e", script).Run() == nil
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
	// Setup App Icon and Cocoa Edit menu (Cmd+C / Cmd+V support)
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
		g.ensureBackend()
		g.syncWindow()
		state, name := "unreachable", ""
		svcLang := ""
		if b, err := g.call("GET", "/api/state", nil); err == nil {
			var d struct {
				Status Status `json:"status"`
				Lang   string `json:"lang"`
				Build  string `json:"build"`
			}
			if json.Unmarshal(b, &d) == nil {
				state = d.Status.State
				name = d.Status.Server
				svcLang = d.Lang
				if d.Build != selfBuildID() && !g.upgradeAsked && (state == stateDisconnected || state == stateError) {
					g.upgradeAsked = true
					go g.offerUpgrade()
				}
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

// offerUpgrade replaces an installed service that comes from another build of the
// program (for example after updating the app) with the one in this app.
func (g *gui) offerUpgrade() {
	if !confirmBox("Hy2Tun", tr("Фоновая служба Hy2Tun устарела. Обновить её сейчас? Потребуется пароль или Touch ID.",
		"The Hy2Tun background service is out of date. Update it now? Your password or Touch ID is required.")) {
		return
	}
	if err := tryElevateInstall(); err != nil {
		msgBox("Hy2Tun", err.Error())
	}
}

// dirWritable reports whether files can be created in dir.
func dirWritable(dir string) bool {
	if os.MkdirAll(dir, 0o755) != nil {
		return false
	}
	f, err := os.CreateTemp(dir, ".w")
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}

// guiLocalDir is where the in-process engine keeps its data: the normal data dir
// when this user can write there, otherwise their own folder (the system one
// belongs to root once the service is installed).
func guiLocalDir(dir string) string {
	if dirWritable(dir) {
		return dir
	}
	u := userDataDir()
	os.MkdirAll(u, 0o755)
	if _, err := os.Stat(filepath.Join(u, "servers.json")); err != nil {
		if b, err := os.ReadFile(filepath.Join(dir, "servers.json")); err == nil {
			os.WriteFile(filepath.Join(u, "servers.json"), b, 0o600)
		}
	}
	return u
}

func cmdGUI(args []string) {
	c, _ := parseFlags(args)
	dir, portable := resolveDataDir(c.data)
	g := &gui{svcAddr: c.ui, svcDir: dir, localDir: guiLocalDir(dir), portable: portable, opts: c.engineOpts(), link: c.link}

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

	// Use the root service if it runs; otherwise serve the window from an engine in
	// this process so it always has something to show.
	g.ensureBackend()

	trayOnly := false
	for _, a := range os.Args[1:] {
		if a == "--tray" || a == "-tray" {
			trayOnly = true
			break
		}
	}

	if !trayOnly {
		go func() {
			time.Sleep(150 * time.Millisecond)
			g.openWindow()
		}()
	}

	systray.Run(g.onReady, g.onExit)
}
