// Hy2Tun-Setup: the installer. It carries Hy2Tun.exe, Uninstall.exe and
// wintun.dll inside (go:embed), copies them to Program Files, registers the
// service and adds a Start menu entry and an "Apps & features" entry. The
// application is started only after the user presses OK.
//
// Build: copy Hy2Tun.exe, Uninstall.exe and wintun.dll into cmd/setup/payload,
// then
//
//	go build -ldflags "-s -w -H=windowsgui" -o Hy2Tun-Setup.exe ./cmd/setup
package main

import (
	"embed"

	"github.com/ActiveDown/hy2tun/internal/setupcore"
)

//go:embed all:payload
var payload embed.FS

// appVersion is overridden at build time: -ldflags "-X main.appVersion=1.2.3"
var appVersion = "0.6.0"

func main() {
	setupcore.Run(setupcore.Options{Mode: setupcore.Install, Version: appVersion, Payload: payload})
}
