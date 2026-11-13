//go:build !android && !windows

package main

import "golang.zx2c4.com/wireguard/tun"

// createTUN creates the TUN adapter itself. On Android the system creates it
// (VpnService) and hands over a file descriptor instead (tun_android.go);
// Windows has its own version that keeps one Wintun adapter identity
// (tun_windows.go).
func createTUN(name string, mtu int) (tun.Device, error) { return tun.CreateTUN(name, mtu) }
