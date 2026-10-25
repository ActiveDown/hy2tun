//go:build windows

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	serviceName = "Hy2Tun"
	serviceDesc = "Hysteria2 VPN client: global TUN with per-application rules"
	runKeyName  = "Hy2Tun"
)

type serviceHandler struct {
	args []string
}

func (h *serviceHandler) Execute(_ []string, req <-chan svc.ChangeRequest, st chan<- svc.Status) (bool, uint32) {
	st <- svc.Status{State: svc.StartPending}
	c, _ := parseFlags(h.args)
	dir, portable := resolveDataDir(c.data)
	ring, closeLog := setupLogging(dir, true)
	defer closeLog()
	log.Printf("service starting, data dir %s", dir)

	e := newEngine(c.engineOpts(), dir, !portable, ring)
	importLink(e, c.link)
	migrateLegacyLink(e)
	api, _, err := startAPI(e, c.ui, !portable)
	if err != nil {
		log.Printf("api: %v", err)
		return true, 1
	}
	defer api.close()

	if e.store.autoConnect() {
		if srv, ok := e.store.get(""); ok {
			log.Printf("auto-connect to %s", srv.Name)
			e.Connect(srv.ID)
		}
	}
	st <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for r := range req {
		switch r.Cmd {
		case svc.Interrogate:
			st <- r.CurrentStatus
		case svc.Stop, svc.Shutdown:
			st <- svc.Status{State: svc.StopPending}
			log.Printf("service stopping")
			e.Shutdown()
			return false, 0
		}
	}
	return false, 0
}

func cmdService(args []string) {
	if err := svc.Run(serviceName, &serviceHandler{args: args}); err != nil {
		fmt.Fprintln(os.Stderr, "service:", err)
		os.Exit(1)
	}
}

// requireAdmin continues only with administrator rights: otherwise the same
// command is started again through the UAC prompt and this process exits.
func requireAdmin() {
	if isAdmin() {
		return
	}
	exe, err := os.Executable()
	if err == nil {
		verb, _ := windows.UTF16PtrFromString("runas")
		file, _ := windows.UTF16PtrFromString(exe)
		args, _ := windows.UTF16PtrFromString(strings.Join(os.Args[1:], " "))
		if windows.ShellExecute(0, verb, file, args, nil, windows.SW_NORMAL) == nil {
			os.Exit(0)
		}
	}
	fmt.Fprintln(os.Stderr, tr("Нужны права администратора. Запустите от имени администратора.", "Administrator rights are required. Run as administrator."))
	os.Exit(1)
}

// cmdInstall registers the service (automatic, delayed start, restart on
// failure) and the per-user tray autostart, then starts the service.
func cmdInstall(args []string) {
	requireAdmin()
	quiet := false
	var svcArgs []string
	for _, a := range args {
		if a == "quiet" {
			quiet = true
		} else {
			svcArgs = append(svcArgs, a)
		}
	}
	args = svcArgs
	exe, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	exe, _ = filepath.Abs(exe)
	m, err := mgr.Connect()
	if err != nil {
		log.Fatal(err)
	}
	defer m.Disconnect()

	if s, err := m.OpenService(serviceName); err == nil {
		s.Control(svc.Stop)
		time.Sleep(time.Second)
		s.Delete()
		s.Close()
		time.Sleep(time.Second)
	}
	s, err := m.CreateService(serviceName, exe, mgr.Config{
		DisplayName:      "Hy2Tun",
		Description:      serviceDesc,
		StartType:        mgr.StartAutomatic,
		DelayedAutoStart: true,
	}, append([]string{"service"}, args...)...)
	if err != nil {
		log.Fatalf("create service: %v", err)
	}
	defer s.Close()
	s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 15 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
	}, 86400)

	// tray / window for every user at logon
	if k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE); err == nil {
		k.SetStringValue(runKeyName, fmt.Sprintf(`"%s" gui --tray`, exe))
		k.Close()
	}
	if err := s.Start(); err != nil {
		log.Printf("service installed, start failed: %v", err)
	}
	fmt.Println(tr("Служба Hy2Tun установлена и запущена. Окно: Hy2Tun gui", "The Hy2Tun service is installed and running. Open the window with: Hy2Tun gui"))
	if !quiet {
		msgBox("Hy2Tun", tr("Служба Hy2Tun установлена и запущена. Откройте окно: Hy2Tun gui", "The Hy2Tun service is installed and running. Open the window with: Hy2Tun gui"))
	}
}

const uninstallKey = `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\Hy2Tun`

// cmdUninstall removes the service and the tray autostart. With --purge (the
// "Apps & features" entry) it also removes the shortcut, the registry entry and
// the program folder.
func cmdUninstall(args []string) {
	requireAdmin()
	purge := false
	for _, a := range args {
		if a == "--purge" || a == "-purge" {
			purge = true
		}
	}
	if purge {
		// full removal has its own window (Uninstall.exe, installed next to this
		// file); older installations that point here are forwarded to it
		if self, err := os.Executable(); err == nil {
			un := filepath.Join(filepath.Dir(self), "Uninstall.exe")
			if _, err := os.Stat(un); err == nil {
				if exec.Command(un, "/uninstall").Start() == nil {
					return
				}
			}
		}
	}
	m, err := mgr.Connect()
	if err != nil {
		log.Fatal(err)
	}
	defer m.Disconnect()
	if s, err := m.OpenService(serviceName); err == nil {
		s.Control(svc.Stop)
		for i := 0; i < 30; i++ {
			if st, err := s.Query(); err == nil && st.State == svc.Stopped {
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
		s.Delete()
		s.Close()
	}
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE); err == nil {
		k.DeleteValue(runKeyName)
		k.Close()
	}
	if !purge {
		fmt.Println(tr("Служба Hy2Tun удалена.", "The Hy2Tun service has been removed."))
		return
	}

	// full removal: close the GUI, drop shortcut + registry entry, then delete
	// the folder from a detached helper because this very file is running.
	self := fmt.Sprintf("PID ne %d", os.Getpid())
	tk := exec.Command("taskkill", "/f", "/im", filepath.Base(os.Args[0]), "/fi", self)
	tk.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	tk.Run()
	os.Remove(filepath.Join(os.Getenv("ProgramData"), `Microsoft\Windows\Start Menu\Programs\Hy2Tun.lnk`))
	registry.DeleteKey(registry.LOCAL_MACHINE, uninstallKey)
	exe, _ := os.Executable()
	dir := filepath.Dir(exe)
	del := exec.Command("cmd", "/c", fmt.Sprintf(`ping -n 3 127.0.0.1 >nul & rd /s /q "%s"`, dir))
	del.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	del.Start()
	msgBox("Hy2Tun", tr("Hy2Tun удалён. Настройки остались в ", "Hy2Tun has been removed. Settings are kept in ")+filepath.Join(os.Getenv("ProgramData"), "Hy2Tun")+tr(" — их можно удалить вручную.", " — delete them by hand if you like."))
}

var _ = windows.Handle(0)
