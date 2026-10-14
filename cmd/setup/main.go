// Hy2Tun-Setup: a self-contained installer. It carries Hy2Tun.exe and
// wintun.dll inside (go:embed), copies them to Program Files, registers the
// service and adds a Start menu entry plus an "Apps & features" uninstall entry.
//
// Build: copy Hy2Tun.exe and wintun.dll into cmd/setup/payload, then
//
//	go build -ldflags "-s -w -H=windowsgui" -o Hy2Tun-Setup.exe ./cmd/setup
package main

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

//go:embed all:payload
var payload embed.FS

const (
	appName      = "Hy2Tun"
	exeName      = "Hy2Tun.exe"
	uninstallKey = `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\Hy2Tun`
	version      = "0.4.0"
)

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

func fail(err error) {
	msgBox(tr("Установка не удалась:", "Setup failed:")+"\n\n"+err.Error(), windows.MB_ICONERROR)
	os.Exit(1)
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
		fail(fmt.Errorf(tr("нужны права администратора: %v", "administrator rights are required: %v"), err))
	}
	os.Exit(0)
}

func main() {
	elevateIfNeeded()

	pf := os.Getenv("ProgramFiles")
	if pf == "" {
		pf = `C:\Program Files`
	}
	dir := filepath.Join(pf, appName)
	target := filepath.Join(dir, exeName)

	// upgrade: remove the old service and stop the old processes first
	if _, err := os.Stat(target); err == nil {
		run(target, "uninstall")
	}
	run("taskkill", "/f", "/im", exeName)
	time.Sleep(700 * time.Millisecond)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		fail(err)
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
		return os.WriteFile(filepath.Join(dir, d.Name()), b, 0o755)
	})
	if err != nil {
		fail(err)
	}
	if count == 0 {
		fail(fmt.Errorf("%s", tr("в установщике нет файлов программы", "the installer contains no program files")))
	}

	// service (automatic, delayed start) + tray autostart; starts the service
	if err := run(target, "install", "quiet"); err != nil {
		fail(err)
	}

	// Start menu shortcut
	lnk := filepath.Join(os.Getenv("ProgramData"), `Microsoft\Windows\Start Menu\Programs`, appName+".lnk")
	ps := fmt.Sprintf(`$s=(New-Object -ComObject WScript.Shell).CreateShortcut('%s');$s.TargetPath='%s';$s.Arguments='gui';$s.WorkingDirectory='%s';$s.IconLocation='%s,0';$s.Description='Hy2Tun';$s.Save()`,
		lnk, target, dir, target)
	run("powershell", "-NoProfile", "-NonInteractive", "-Command", ps)

	// Apps & features entry
	if k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, uninstallKey, registry.SET_VALUE); err == nil {
		k.SetStringValue("DisplayName", appName)
		k.SetStringValue("DisplayVersion", version)
		k.SetStringValue("Publisher", appName)
		k.SetStringValue("InstallLocation", dir)
		k.SetStringValue("DisplayIcon", target)
		k.SetStringValue("UninstallString", fmt.Sprintf(`"%s" uninstall --purge`, target))
		k.SetDWordValue("NoModify", 1)
		k.SetDWordValue("NoRepair", 1)
		k.Close()
	}

	// open the window for the person who ran the installer (unelevated is not
	// possible from here; the tray autostart does it at the next logon)
	exec.Command(target, "gui").Start()
	msgBox(tr("Hy2Tun установлен.\n\nСлужба работает и стартует вместе с Windows. Окно открыто; значок — в трее. Добавьте ссылку hy2:// на вкладке «Серверы» и нажмите «Подключить».",
		"Hy2Tun is installed.\n\nThe service is running and starts with Windows. The window is open; the icon is in the tray. Add a hy2:// link on the Servers tab and press Connect."), windows.MB_ICONINFORMATION)
}
