//go:build !android

package main

import "golang.zx2c4.com/wireguard/tun"

// createTUN creates the TUN adapter itself. On Android the system creates it
// (VpnService) and hands over a file descriptor instead (tun_android.go).
func createTUN(name string, mtu int) (tun.Device, error) { return tun.CreateTUN(name, mtu) }
