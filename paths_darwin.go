//go:build darwin

package main

import (
	"os"
	"path/filepath"
)

func defaultDataDir() string {
	sys := "/Library/Application Support/Hy2Tun"
	if _, err := os.Stat(sys); err == nil {
		return sys
	}
	if os.Geteuid() == 0 {
		return sys
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/tmp"
	}
	return filepath.Join(home, "Library", "Application Support", "Hy2Tun")
}

func lockDownFile(path string, usersRead bool) {
	if usersRead {
		os.Chmod(path, 0o644)
	} else {
		os.Chmod(path, 0o600)
	}
}
