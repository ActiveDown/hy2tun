//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Foundation
#import <Foundation/Foundation.h>
#include <stdlib.h>
#include <string.h>

// Returns the main executable of an .app bundle (caller frees), or NULL.
char *bundleExecutablePath(const char *path) {
    @autoreleasepool {
        NSBundle *b = [NSBundle bundleWithPath:[NSString stringWithUTF8String:path]];
        NSString *e = b.executablePath;
        return e ? strdup([e UTF8String]) : NULL;
    }
}
*/
import "C"

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"
)

var (
	appsMu    sync.Mutex
	appsCache []installedApp
	appsAt    time.Time
)

func appSearchDirs() []string {
	dirs := []string{
		"/Applications",
		"/System/Applications",
		"/System/Volumes/Preboot/Cryptexes/App/System/Applications", // Safari lives here since macOS 13
	}
	if u := getActiveMacUser(); u != "" {
		dirs = append(dirs, filepath.Join("/Users", u, "Applications"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, "Applications"))
	}
	return dirs
}

// scanApps lists .app bundles in dir and in its plain sub-folders (Utilities, vendor folders).
func scanApps(dir string, depth int, out map[string]installedApp) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		full := filepath.Join(dir, e.Name())
		switch {
		case strings.HasSuffix(e.Name(), ".app"):
			cp := C.CString(full)
			ce := C.bundleExecutablePath(cp)
			C.free(unsafe.Pointer(cp))
			if ce == nil {
				continue
			}
			exe := C.GoString(ce)
			C.free(unsafe.Pointer(ce))
			name := strings.ToLower(filepath.Base(exe))
			if _, dup := out[name]; !dup {
				out[name] = installedApp{Label: strings.TrimSuffix(e.Name(), ".app"), Name: name, Path: full}
			}
		case e.IsDir() && depth > 0 && !strings.HasPrefix(e.Name(), "."):
			scanApps(full, depth-1, out)
		}
	}
}

// installedApps returns the applications found in the usual folders (cached for a minute).
func installedApps() []installedApp {
	appsMu.Lock()
	defer appsMu.Unlock()
	if appsCache != nil && time.Since(appsAt) < time.Minute {
		return appsCache
	}
	found := map[string]installedApp{}
	for _, d := range appSearchDirs() {
		scanApps(d, 1, found)
	}
	list := make([]installedApp, 0, len(found))
	for _, a := range found {
		list = append(list, a)
	}
	sort.Slice(list, func(i, j int) bool {
		return strings.ToLower(list[i].Label) < strings.ToLower(list[j].Label)
	})
	appsCache, appsAt = list, time.Now()
	return list
}
