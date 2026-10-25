//go:build darwin

package main

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

func shCmd(name, script string) diagCmd {
	return diagCmd{name: name, argv: []string{"/bin/sh", "-c", script}}
}

// diagCommands is the macOS network snapshot.
func diagCommands() []diagCmd {
	return []diagCmd{
		{"sw_vers", []string{"sw_vers"}},
		{"ifconfig", []string{"ifconfig", "-a"}},
		{"routes-v4", []string{"netstat", "-rn", "-f", "inet"}},
		{"routes-v6", []string{"netstat", "-rn", "-f", "inet6"}},
		{"route-default", []string{"route", "-n", "get", "default"}},
		{"dns", []string{"scutil", "--dns"}},
		{"proxy", []string{"scutil", "--proxy"}},
		{"nwi", []string{"scutil", "--nwi"}},
		{"vpn-services", []string{"scutil", "--nc", "list"}},
		{"network-services", []string{"networksetup", "-listallnetworkservices"}},
		shCmd("launchd-service", `launchctl print system/com.activedown.hy2tun | head -40`),
		shCmd("vpn-processes", `ps -axo pid,user,comm | grep -iE 'vpn|wireguard|openvpn|nekoray|nekobox|v2ray|xray|clash|sing-box|hysteria|tailscale|zerotier|amnezia|v2box|warp|proxy' | grep -v grep`),
		shCmd("connections", `lsof -nP -iTCP -sTCP:ESTABLISHED | head -250`),
	}
}

func diagRun(timeout time.Duration, argv []string) string {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, argv[0], argv[1:]...).CombinedOutput()
	s := string(out)
	if err != nil {
		s += fmt.Sprintf("\n[error: %v]\n", err)
	}
	return s
}
