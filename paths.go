package main

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"sync"
)

var (
	buildOnce sync.Once
	buildSum  string
)

// selfBuildID identifies the running executable (a short hash of the file), so
// the desktop app can tell whether the installed service is the same build.
func selfBuildID() string {
	buildOnce.Do(func() {
		exe, err := os.Executable()
		if err != nil {
			return
		}
		f, err := os.Open(exe)
		if err != nil {
			return
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err == nil {
			buildSum = hex.EncodeToString(h.Sum(nil)[:8])
		}
	})
	return buildSum
}

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

// tokenDirs lists the folders where a running service may keep its control token.
func tokenDirs(dir string) []string {
	return append([]string{defaultDataDir()}, dir)
}
