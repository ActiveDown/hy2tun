//go:build darwin

package main

import (
	"encoding/json"
	"fmt"
	"html"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	plistLabel = "com.activedown.hy2tun"
	plistPath  = "/Library/LaunchDaemons/com.activedown.hy2tun.plist"

	systemDataDir  = "/Library/Application Support/Hy2Tun"
	serviceAPIAddr = "127.0.0.1:7878"
)

func getActiveMacUser() string {
	if u := os.Getenv("SUDO_USER"); u != "" {
		return u
	}
	out, err := exec.Command("stat", "-f", "%Su", "/dev/console").Output()
	if err == nil {
		name := strings.TrimSpace(string(out))
		if name != "" && name != "root" {
			return name
		}
	}
	return ""
}

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

	// Stop a running daemon first: its binary is about to be replaced.
	_ = exec.Command("launchctl", "unload", plistPath).Run()

	if exe != destExe {
		// Copy to a new file and rename it over the old one: overwriting a signed
		// executable in place makes macOS kill it with "Code Signature Invalid".
		if err := copyExecutable(exe, destExe); err != nil {
			log.Printf("warn: copy binary: %v (running from %s)", err, exe)
			destExe = exe
		}
	}

	dataDir := defaultDataDir()
	os.MkdirAll(dataDir, 0o755)

	// Servers and settings configured in the user's home directory move to the system dir.
	var userDirs []string
	if u := getActiveMacUser(); u != "" {
		userDirs = append(userDirs, filepath.Join("/Users", u, "Library", "Application Support", "Hy2Tun"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		userDirs = append(userDirs, filepath.Join(home, "Library", "Application Support", "Hy2Tun"))
	}
	for _, uDir := range userDirs {
		if uDir == dataDir {
			continue
		}
		migrateUserData(uDir, dataDir)
	}

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
		xmlArgs.WriteString(fmt.Sprintf("        <string>%s</string>\n", html.EscapeString(a)))
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
`, plistLabel, xmlArgs.String(), html.EscapeString(dataDir), html.EscapeString(dataDir))

	if err := os.WriteFile(plistPath, []byte(plistContent), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "install error writing plist: %v\n", err)
		os.Exit(1)
	}

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

// copyExecutable writes src next to dst and renames it into place.
func copyExecutable(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	tmp := dst + ".new"
	if err := os.WriteFile(tmp, b, 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// migrateUserData brings servers and settings of a per-user data dir into the
// system one. Servers are merged by link so nothing already there is lost.
func migrateUserData(src, dst string) {
	var in, out storeData
	if b, err := os.ReadFile(filepath.Join(src, "servers.json")); err == nil && json.Unmarshal(b, &in) == nil {
		if b, err := os.ReadFile(filepath.Join(dst, "servers.json")); err == nil {
			json.Unmarshal(b, &out)
		}
		have := map[string]bool{}
		ids := map[string]bool{}
		for _, s := range out.Servers {
			have[s.Link], ids[s.ID] = true, true
		}
		added := 0
		for _, s := range in.Servers {
			if have[s.Link] || ids[s.ID] {
				continue
			}
			out.Servers = append(out.Servers, s)
			added++
		}
		if out.Last == "" {
			out.Last = in.Last
		}
		if out.Lang == "" {
			out.Lang = in.Lang
		}
		out.AutoConnect = out.AutoConnect || in.AutoConnect
		if added > 0 || out.Last != "" {
			if b, err := json.MarshalIndent(out, "", "  "); err == nil && os.WriteFile(filepath.Join(dst, "servers.json"), b, 0o600) == nil {
				log.Printf("migrated %d server(s) from %s", added, src)
			}
		}
	}
	dstSettings := filepath.Join(dst, "settings.json")
	if _, err := os.Stat(dstSettings); os.IsNotExist(err) {
		if b, err := os.ReadFile(filepath.Join(src, "settings.json")); err == nil {
			_ = os.WriteFile(dstSettings, b, 0o644)
			log.Printf("migrated settings from %s", src)
		}
	}
}

func appleScriptQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// tryElevateInstall installs the launchd daemon through the standard macOS
// Touch ID / password dialog.
func tryElevateInstall() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	script := fmt.Sprintf(`do shell script (quoted form of %s) & " install" with administrator privileges`, appleScriptQuote(exe))
	cmd := exec.Command("osascript", "-e", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Printf("elevate error: %v: %s", err, out)
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// handoffConnect waits for the freshly installed service to answer and asks it
// to connect to the given server.
func handoffConnect(id string) error {
	deadline := time.Now().Add(20 * time.Second)
	for {
		tok := readTokenFile(systemDataDir)
		if tok != "" {
			if _, err := apiCall(serviceAPIAddr, tok, "GET", "/api/state", nil); err == nil {
				_, err := apiCall(serviceAPIAddr, tok, "POST", "/api/connect", map[string]string{"id": id})
				return err
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the service did not start (see %s/hy2tun.log)", systemDataDir)
		}
		time.Sleep(300 * time.Millisecond)
	}
}
