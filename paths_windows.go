//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

func defaultDataDir() string {
	pd := os.Getenv("ProgramData")
	if pd == "" {
		pd = `C:\ProgramData`
	}
	return filepath.Join(pd, "Hy2Tun")
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
