package main

import (
	"os"
	"path/filepath"
)

// resolveDataDir decides where settings, servers and the control token live.
//
//   - -data <dir> wins;
//   - "portable" mode: if hy2.url / servers.json / a file named "portable" sits
//     next to the executable, that folder is used (the test stand works this way);
//   - otherwise system default data directory.
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
	return defaultDataDir(), false
}
