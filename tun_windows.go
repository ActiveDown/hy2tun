//go:build windows

package main

import (
	"crypto/md5"
	"encoding/binary"
	"fmt"
	"log"
	"regexp"
	"strings"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wireguard/tun"
)

// tunGUID is the adapter identity requested from wintun. Wintun otherwise picks
// a random GUID for every adapter, and Windows counts each one as a new network:
// every connect creates another NLA profile, which shows up as a growing
// "Hy2Tun 38", "Hy2Tun 39", ... next to the adapter. A GUID derived from the
// adapter name makes the profile stable. Version 3 (name based) and the RFC 4122
// variant are set only so the value is a well-formed UUID.
func tunGUID(name string) *windows.GUID {
	sum := md5.Sum([]byte("hy2tun/wintun/" + strings.ToLower(name)))
	var g windows.GUID
	g.Data1 = binary.BigEndian.Uint32(sum[0:4])
	g.Data2 = binary.BigEndian.Uint16(sum[4:6])
	g.Data3 = binary.BigEndian.Uint16(sum[6:8])&0x0fff | 0x3000
	copy(g.Data4[:], sum[8:16])
	g.Data4[0] = g.Data4[0]&0x3f | 0x80
	return &g
}

// tunCleanupScript removes what earlier runs left behind: an adapter still
// present after a crash, and the connection-name entries of adapters that are
// long gone (their duplicate names would push a new adapter to a suffixed
// name). The entry of our own GUID is kept: it belongs to the adapter identity
// we are going to reuse.
func tunCleanupScript(name string, guid *windows.GUID) string {
	// PowerShell single-quoted strings escape a quote by doubling it.
	pat := strings.ReplaceAll("^"+regexp.QuoteMeta(name)+`( \d+)?$`, "'", "''")
	const script = `$ErrorActionPreference='SilentlyContinue';` +
		` $pat='%[1]s'; $keep='%[2]s';` +
		` Get-NetAdapter -IncludeHidden | Where-Object { $_.Name -match $pat -and $_.InterfaceDescription -match 'Wintun|WireGuard' } | ForEach-Object {` +
		`  'removed adapter: ' + $_.Name; Remove-NetAdapter -InputObject $_ -Confirm:$false };` +
		` $root='HKLM:\SYSTEM\CurrentControlSet\Control\Network\{4D36E972-E325-11CE-BFC1-08002BE10318}';` +
		` Get-ChildItem $root | Where-Object { $_.PSChildName -match '^\{[0-9A-Fa-f-]{36}\}$' -and $_.PSChildName -ne $keep } | ForEach-Object {` +
		`  $c = Get-ItemProperty -LiteralPath ($_.PSPath + '\Connection') -ErrorAction SilentlyContinue;` +
		`  if ($c -and $c.Name -match $pat) { Remove-Item -LiteralPath $_.PSPath -Recurse -Force -ErrorAction SilentlyContinue }` +
		` }; 'ok'`
	return fmt.Sprintf(script, pat, guid.String())
}

// createTUN creates the Wintun adapter. It reuses one identity across runs
// (see tunGUID) after sweeping away the leftovers of earlier ones.
func createTUN(name string, mtu int) (tun.Device, error) {
	guid := tunGUID(name)
	if out, err := ps(tunCleanupScript(name, guid)); err != nil {
		log.Printf("warn: stale adapter cleanup: %v: %s", err, out)
	} else if strings.Contains(out, "removed adapter") {
		log.Printf("tun: %s", out)
	}
	return tun.CreateTUNWithRequestedGUID(name, guid, mtu)
}
