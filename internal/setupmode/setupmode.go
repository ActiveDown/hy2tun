// Package setupmode decides whether the setup executable should install or
// uninstall. It lives in its own package so it can be unit-tested: the setup
// executable itself carries a "requireAdministrator" manifest and cannot be run
// by "go test" without elevation.
package setupmode

import (
	"path/filepath"
	"strings"
)

// IsUninstall reports whether this run is an uninstall.
//
// It is one when the command line asks for it (/uninstall), or when the file
// is named like an uninstaller: Uninstall.exe in the installation folder (the
// user double-clicks it without arguments) or the temporary copy the
// uninstaller starts from (Hy2Tun-Uninstall-<pid>.exe).
func IsUninstall(args []string, exePath string) bool {
	for _, a := range args {
		switch strings.ToLower(a) {
		case "/uninstall", "--uninstall", "-uninstall":
			return true
		}
	}
	base := strings.ToLower(filepath.Base(exePath))
	return strings.HasPrefix(base, "uninstall") || strings.HasPrefix(base, "hy2tun-uninstall-")
}

// SamePath reports whether two Windows paths denote the same file name.
func SamePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}
