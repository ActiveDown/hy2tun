//go:build darwin

package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
)

const (
	plistLabel = "com.activedown.hy2tun"
	plistPath  = "/Library/LaunchDaemons/com.activedown.hy2tun.plist"
)

func requireAdmin() {
	if isAdmin() {
		return
	}
	fmt.Fprintln(os.Stderr, tr("Нужны права root. Запустите через sudo.", "Root rights are required. Run with sudo."))
	os.Exit(1)
}

func cmdService(args []string) {
	requireAdmin()
	c, _ := parseFlags(args)
	dir, portable := resolveDataDir(c.data)
	ring, closeLog := setupLogging(dir, true)
	defer closeLog()
	log.Printf("daemon starting, data dir %s", dir)

	e := newEngine(c.engineOpts(), dir, !portable, ring)
	importLink(e, c.link)
	migrateLegacyLink(e)
	api, _, err := startAPI(e, c.ui, !portable)
	if err != nil {
		log.Fatalf("api: %v", err)
	}
	defer api.close()

	if e.store.autoConnect() {
		if srv, ok := e.store.get(""); ok {
			log.Printf("auto-connect to %s", srv.Name)
			e.Connect(srv.ID)
		}
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	log.Printf("daemon stopping")
	e.Shutdown()
}

func cmdInstall(args []string) {
	requireAdmin()
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "install: %v\n", err)
		os.Exit(1)
	}

	destExe := "/usr/local/bin/hy2tun"
	os.MkdirAll("/usr/local/bin", 0o755)
	if exe != destExe {
		// Copy binary to /usr/local/bin/hy2tun
		b, err := os.ReadFile(exe)
		if err == nil {
			_ = os.WriteFile(destExe, b, 0o755)
		} else {
			destExe = exe
		}
	}

	dataDir := defaultDataDir()
	os.MkdirAll(dataDir, 0o755)

	var extraArgs []string
	for _, a := range args {
		if a != "-q" && a != "--quiet" {
			extraArgs = append(extraArgs, a)
		}
	}

	progArgs := []string{destExe, "service"}
	progArgs = append(progArgs, extraArgs...)

	var xmlArgs strings.Builder
	for _, a := range progArgs {
		xmlArgs.WriteString(fmt.Sprintf("        <string>%s</string>\n", a))
	}

	plistContent := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
%s    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>%s/hy2tun.log</string>
    <key>StandardErrorPath</key>
    <string>%s/hy2tun.log</string>
</dict>
</plist>
`, plistLabel, xmlArgs.String(), dataDir, dataDir)

	if err := os.WriteFile(plistPath, []byte(plistContent), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "install error writing plist: %v\n", err)
		os.Exit(1)
	}

	_ = exec.Command("launchctl", "unload", plistPath).Run()
	if out, err := exec.Command("launchctl", "load", "-w", plistPath).CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "launchctl load failed: %v: %s\n", err, out)
		os.Exit(1)
	}

	fmt.Println(tr("Служба Hy2Tun успешно установлена и запущена через launchd.", "Hy2Tun service successfully installed and started via launchd."))
}

func cmdUninstall(args []string) {
	requireAdmin()
	_ = exec.Command("launchctl", "unload", "-w", plistPath).Run()
	_ = os.Remove(plistPath)
	fmt.Println(tr("Служба Hy2Tun остановлена и удалена.", "Hy2Tun service stopped and removed."))
}
