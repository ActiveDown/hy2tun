// Uninstall: the Hy2Tun uninstaller. It is a program of its own (not a mode of
// the installer), so nothing about its behaviour depends on its file name or
// on command-line arguments. The installer stores it as
// C:\Program Files\Hy2Tun\Uninstall.exe and points "Apps & features" to it.
//
//	go build -ldflags "-s -w -H=windowsgui" -o Uninstall.exe ./cmd/uninstall
package main

import "github.com/ActiveDown/hy2tun/internal/setupcore"

// appVersion is overridden at build time: -ldflags "-X main.appVersion=1.2.3"
var appVersion = "0.5.2"

func main() {
	setupcore.Run(setupcore.Options{Mode: setupcore.Uninstall, Version: appVersion})
}
