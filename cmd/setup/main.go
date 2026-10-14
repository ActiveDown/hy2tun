// Hy2Tun-Setup: a self-contained installer with its own window. It carries
// Hy2Tun.exe and wintun.dll inside (go:embed), copies them to Program Files,
// registers the service and adds a Start menu entry plus an "Apps & features"
// uninstall entry. The application is started only after the user presses OK.
//
// Build: copy Hy2Tun.exe and wintun.dll into cmd/setup/payload, then
//
//	go build -ldflags "-s -w -H=windowsgui" -o Hy2Tun-Setup.exe ./cmd/setup
package main

import (
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ActiveDown/hy2tun/internal/logo"
	"github.com/ActiveDown/hy2tun/internal/winui"
	"github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

//go:embed all:payload
var payload embed.FS

const (
	appName      = "Hy2Tun"
	exeName      = "Hy2Tun.exe"
	uninstallKey = `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\Hy2Tun`
)

// appVersion is overridden at build time: -ldflags "-X main.appVersion=1.2.3"
var appVersion = "0.5.0"

// setupLang is English unless the user already chose Russian in the app
// (stored in %ProgramData%\Hy2Tun\servers.json by an earlier installation).
var setupLang = func() string {
	b, err := os.ReadFile(filepath.Join(os.Getenv("ProgramData"), "Hy2Tun", "servers.json"))
	if err == nil && strings.Contains(string(b), `"lang": "ru"`) {
		return "ru"
	}
	return "en"
}()

func tr(ru, en string) string {
	if setupLang == "ru" {
		return ru
	}
	return en
}

func msgBox(text string, icon uint32) {
	t, _ := windows.UTF16PtrFromString(appName + tr(" — установка", " — setup"))
	m, _ := windows.UTF16PtrFromString(text)
	windows.MessageBox(0, m, t, windows.MB_OK|icon)
}

func run(name string, args ...string) error {
	c := exec.Command(name, args...)
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := c.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func elevateIfNeeded() {
	if windows.GetCurrentProcessToken().IsElevated() {
		return
	}
	exe, _ := os.Executable()
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	args, _ := windows.UTF16PtrFromString(strings.Join(os.Args[1:], " "))
	if err := windows.ShellExecute(0, verb, file, args, nil, windows.SW_NORMAL); err != nil {
		msgBox(fmt.Sprintf(tr("Нужны права администратора: %v", "Administrator rights are required: %v"), err), windows.MB_ICONERROR)
	}
	os.Exit(0)
}

// ---- the installation itself ----

type step struct {
	ru, en string
	do     func(*installer) error
}

type installer struct {
	dir, target, lnk string
}

func newInstaller() *installer {
	pf := os.Getenv("ProgramFiles")
	if pf == "" {
		pf = `C:\Program Files`
	}
	dir := filepath.Join(pf, appName)
	return &installer{
		dir:    dir,
		target: filepath.Join(dir, exeName),
		lnk:    filepath.Join(os.Getenv("ProgramData"), `Microsoft\Windows\Start Menu\Programs`, appName+".lnk"),
	}
}

var steps = []step{
	{"Останавливаем предыдущую версию…", "Stopping the previous version…", func(in *installer) error {
		if _, err := os.Stat(in.target); err == nil {
			run(in.target, "uninstall") // upgrade: removes the old service
		}
		run("taskkill", "/f", "/im", exeName)
		time.Sleep(700 * time.Millisecond)
		return nil
	}},
	{"Копируем файлы…", "Copying files…", func(in *installer) error {
		if err := os.MkdirAll(in.dir, 0o755); err != nil {
			return err
		}
		count := 0
		err := fs.WalkDir(payload, "payload", func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || d.Name() == ".keep" {
				return err
			}
			b, err := payload.ReadFile(p)
			if err != nil {
				return err
			}
			count++
			return os.WriteFile(filepath.Join(in.dir, d.Name()), b, 0o755)
		})
		if err != nil {
			return err
		}
		if count == 0 {
			return fmt.Errorf("%s", tr("в установщике нет файлов программы", "the installer contains no program files"))
		}
		return nil
	}},
	{"Устанавливаем и запускаем службу…", "Installing and starting the service…", func(in *installer) error {
		return run(in.target, "install", "quiet") // service (delayed automatic start) + tray autostart
	}},
	{"Создаём ярлык в меню «Пуск»…", "Creating the Start menu shortcut…", func(in *installer) error {
		ps := fmt.Sprintf(`$s=(New-Object -ComObject WScript.Shell).CreateShortcut('%s');$s.TargetPath='%s';$s.Arguments='gui';$s.WorkingDirectory='%s';$s.IconLocation='%s,0';$s.Description='Hy2Tun';$s.Save()`,
			in.lnk, in.target, in.dir, in.target)
		run("powershell", "-NoProfile", "-NonInteractive", "-Command", ps)
		return nil // a missing shortcut is not fatal
	}},
	{"Регистрируем программу в системе…", "Registering the program…", func(in *installer) error {
		k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, uninstallKey, registry.SET_VALUE)
		if err != nil {
			return nil
		}
		defer k.Close()
		k.SetStringValue("DisplayName", appName)
		k.SetStringValue("DisplayVersion", appVersion)
		k.SetStringValue("Publisher", appName)
		k.SetStringValue("InstallLocation", in.dir)
		k.SetStringValue("DisplayIcon", in.target)
		k.SetStringValue("UninstallString", fmt.Sprintf(`"%s" uninstall --purge`, in.target))
		k.SetDWordValue("NoModify", 1)
		k.SetDWordValue("NoRepair", 1)
		return nil
	}},
}

// launchApp starts the window as the normal (non-elevated) user through the
// shortcut; the installer itself runs elevated.
func (in *installer) launchApp() {
	if _, err := os.Stat(in.lnk); err == nil {
		exec.Command("explorer.exe", in.lnk).Start()
		return
	}
	exec.Command(in.target, "gui").Start()
}

// ---- the window ----

type stepText struct {
	RU string `json:"ru"`
	EN string `json:"en"`
}

func page() string {
	var st []stepText
	for _, s := range steps {
		st = append(st, stepText{s.ru, s.en})
	}
	b, _ := json.Marshal(st)
	html := strings.Replace(setupHTML, "/*STEPS*/[]", string(b), 1)
	return strings.Replace(html, "/*LOGO*/", base64.StdEncoding.EncodeToString(logo.PNG(logo.App(176))), 1)
}

func main() {
	elevateIfNeeded()
	in := newInstaller()

	data := filepath.Join(os.TempDir(), "Hy2TunSetup-webview2")
	os.MkdirAll(data, 0o755)
	w := webview2.NewWithOptions(webview2.WebViewOptions{
		DataPath: data,
		WindowOptions: webview2.WindowOptions{
			Title: appName + tr(" — установка", " — Setup"), Width: 520, Height: 430, Center: true, IconId: 2,
		},
	})
	if w == nil {
		in.runHeadless() // no WebView2 runtime: plain message boxes
		return
	}
	hwnd := w.Window()
	winui.ApplyTheme(hwnd, winui.SystemTheme())
	winui.MakeFixedSize(hwnd)
	winui.SetCloseEnabled(hwnd, false) // no X while files are being copied
	w.SetSize(520, 430, webview2.HintFixed)

	launch := false
	finished := false
	w.Bind("setupFinish", func(l bool) {
		launch = l
		w.Dispatch(func() { w.Terminate() })
	})
	w.Init(fmt.Sprintf("window.__lang=%q;", setupLang))
	w.SetHtml(page())

	go func() {
		time.Sleep(400 * time.Millisecond) // let the page load
		for i, s := range steps {
			i := i
			w.Dispatch(func() { w.Eval(fmt.Sprintf("progress(%d)", i)) })
			if err := s.do(in); err != nil {
				msg, _ := json.Marshal(err.Error())
				w.Dispatch(func() {
					winui.SetCloseEnabled(hwnd, true)
					w.Eval("fail(" + string(msg) + ")")
				})
				return
			}
		}
		finished = true
		w.Dispatch(func() {
			winui.SetCloseEnabled(hwnd, true)
			w.Eval("done()")
		})
	}()

	w.Run()
	w.Destroy()
	// only now, after the user pressed OK, the application starts
	if finished && launch {
		in.launchApp()
	}
	if !finished {
		os.Exit(1)
	}
}

// runHeadless is the fallback without WebView2.
func (in *installer) runHeadless() {
	for _, s := range steps {
		if err := s.do(in); err != nil {
			msgBox(tr("Установка не удалась:", "Setup failed:")+"\n\n"+err.Error(), windows.MB_ICONERROR)
			os.Exit(1)
		}
	}
	msgBox(tr("Hy2Tun установлен.\n\nСлужба работает и стартует вместе с Windows. Нажмите OK — откроется окно приложения.",
		"Hy2Tun is installed.\n\nThe service is running and starts with Windows. Press OK to open the application."), windows.MB_ICONINFORMATION)
	in.launchApp()
}

const setupHTML = `<!doctype html><html><head><meta charset="utf-8"><style>
:root{--bg:#0e1015;--card:#171a21;--line:#272c38;--tx:#eceff5;--mut:#8b93a5;--ok:#3ddc84;--acc:#7a5cff;--bad:#ef5350}
@media (prefers-color-scheme: light){:root{--bg:#f2f4f8;--card:#fff;--line:#dde2ec;--tx:#171a21;--mut:#667085;--ok:#12a057;--acc:#5b3df5;--bad:#c92a2a}}
*{box-sizing:border-box}html,body{height:100%;margin:0}
body{background:var(--bg);color:var(--tx);font:15px/1.45 "Segoe UI",system-ui,sans-serif;display:flex;flex-direction:column;user-select:none;overflow:hidden}
.lang{position:absolute;top:10px;right:14px;background:transparent;border:1px solid var(--line);color:var(--mut);padding:3px 10px;border-radius:999px;font:600 12px "Segoe UI";cursor:pointer}
.lang:hover{color:var(--tx)}
.wrap{flex:1;display:flex;flex-direction:column;align-items:center;justify-content:center;padding:28px 40px;text-align:center}
.busy{display:flex;flex-direction:column;align-items:center;width:100%}
.logo{width:88px;height:88px;margin-bottom:20px;filter:drop-shadow(0 12px 26px rgba(122,92,255,.38))}
h1{font-size:22px;margin:0 0 6px}
.sub{color:var(--mut);font-size:14px;min-height:42px;max-width:420px}
.bar{width:100%;max-width:380px;height:8px;background:var(--line);border-radius:8px;margin:22px 0 10px;overflow:hidden}
.bar i{display:block;height:100%;width:0;background:linear-gradient(90deg,#7a5cff,#00c2ff);border-radius:8px;transition:width .35s}
.step{color:var(--mut);font-size:13px;min-height:20px}
.final{display:none;flex-direction:column;align-items:center;width:100%}
.final.on{display:flex}.busy.off{display:none}
.chk{display:flex;gap:8px;align-items:center;margin:16px 0 4px;color:var(--tx);font-size:14px;cursor:pointer}
.ok{margin-top:14px;min-width:140px;background:var(--acc);color:#fff;border:0;border-radius:12px;padding:11px 26px;font:600 15px "Segoe UI";cursor:pointer}
.ok:hover{filter:brightness(1.08)}
.err{color:var(--bad);font-size:13px;margin-top:10px;word-break:break-word;max-height:90px;overflow:auto;user-select:text}
.tick{width:64px;height:64px;border-radius:50%;background:var(--ok);display:grid;place-items:center;margin-bottom:18px;color:#06210f;font-size:34px;font-weight:700}
.tick.bad{background:var(--bad);color:#fff}
</style></head><body>
<button class="lang" id="lang"></button>
<div class="wrap">
  <div class="busy" id="busy">
    <img class="logo" alt="" src="data:image/png;base64,/*LOGO*/">
    <h1 id="t1"></h1>
    <div class="sub" id="s1"></div>
    <div class="bar"><i id="bar"></i></div>
    <div class="step" id="step"></div>
  </div>
  <div class="final" id="final">
    <div class="tick" id="tick">✓</div>
    <h1 id="t2"></h1>
    <div class="sub" id="s2"></div>
    <div class="err" id="err"></div>
    <label class="chk" id="chkrow"><input type="checkbox" id="chk" checked><span id="chkl"></span></label>
    <button class="ok" id="ok">OK</button>
  </div>
</div>
<script>
const STEPS=/*STEPS*/[];
const D={
 en:{t1:'Installing Hy2Tun',s1:'Please wait. This takes a few seconds.',t2:'Hy2Tun is installed',s2:'The service is running and starts together with Windows. Press OK to open the application.',chk:'Open Hy2Tun now',bad:'Installation failed',badS:'Nothing was changed that you need to undo. You can run the installer again.',ok:'OK',close:'Close'},
 ru:{t1:'Установка Hy2Tun',s1:'Подождите несколько секунд.',t2:'Hy2Tun установлен',s2:'Служба работает и стартует вместе с Windows. Нажмите OK — откроется приложение.',chk:'Открыть Hy2Tun сейчас',bad:'Установка не удалась',badS:'Ничего вручную откатывать не нужно. Можно запустить установщик ещё раз.',ok:'OK',close:'Закрыть'}
};
let lang=window.__lang||'en', cur=0, state='busy';
const $=id=>document.getElementById(id);
function render(){
  const d=D[lang];
  $('lang').textContent=lang==='ru'?'EN':'RU';
  $('t1').textContent=d.t1;$('s1').textContent=d.s1;
  $('step').textContent=STEPS[cur]?STEPS[cur][lang]:'';
  if(state==='done'){$('t2').textContent=d.t2;$('s2').textContent=d.s2;$('chkl').textContent=d.chk;$('ok').textContent=d.ok}
  if(state==='fail'){$('t2').textContent=d.bad;$('s2').textContent=d.badS;$('ok').textContent=d.close}
}
function progress(i){cur=i;$('bar').style.width=Math.round((i+0.5)/STEPS.length*100)+'%';render()}
function done(){state='done';$('bar').style.width='100%';$('busy').classList.add('off');$('final').classList.add('on');$('ok').focus();render()}
function fail(msg){state='fail';$('busy').classList.add('off');$('final').classList.add('on');
  $('tick').textContent='!';$('tick').classList.add('bad');$('chkrow').style.display='none';$('err').textContent=msg;render()}
$('ok').onclick=()=>setupFinish(state==='done'&&$('chk').checked);
$('lang').onclick=()=>{lang=lang==='ru'?'en':'ru';render()};
document.addEventListener('keydown',e=>{if(e.key==='Enter'&&state!=='busy')$('ok').click()});
render();
</script></body></html>`
