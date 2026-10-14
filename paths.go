package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// resolveDataDir decides where settings, servers and the control token live.
//
//   - -data <dir> wins;
//   - "portable" mode: if hy2.url / servers.json / a file named "portable" sits
//     next to the executable, that folder is used (the test stand works this way);
//   - otherwise %ProgramData%\Hy2Tun (installed service).
func resolveDataDir(flagVal string) (dir string, portable bool) {
	if flagVal != "" {
		return flagVal, true
	}
	exe, _ := os.Executable()
	d := filepath.Dir(exe)
	for _, f := range []string{"hy2.url", "servers.json", "portable"} {
		if _, err := os.Stat(filepath.Join(d, f)); err == nil {
			return d, true
		}
	}
	pd := os.Getenv("ProgramData")
	if pd == "" {
		pd = `C:\ProgramData`
	}
	return filepath.Join(pd, "Hy2Tun"), false
}

// Well-known SIDs, so the ACLs do not depend on the Windows language.
const (
	sidSystem = "*S-1-5-18"
	sidAdmins = "*S-1-5-32-544"
	sidUsers  = "*S-1-5-32-545"
)

// lockDownFile limits a file to SYSTEM and Administrators; with usersRead the
// local Users group may read it as well (used for the control token).
func lockDownFile(path string, usersRead bool) {
	args := []string{path, "/inheritance:r", "/grant:r", sidSystem + ":F", sidAdmins + ":F"}
	if usersRead {
		args = append(args, "/grant:r", sidUsers+":R")
	}
	cmd := exec.Command("icacls", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	cmd.Run()
}
