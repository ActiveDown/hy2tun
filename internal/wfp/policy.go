// Package wfp installs Hy2Tun's Windows Filtering Platform rules.
//
// The low-level types and syscall wrappers in this package are taken from
// WireGuard for Windows (tunnel/firewall, MIT license, see
// LICENSE-wireguard-windows.txt). The policy below is Hy2Tun's own.
//
// All objects live in a *dynamic* WFP session: Windows removes every filter
// automatically when the session handle is closed or the process dies, so a
// crash can never leave the machine without DNS.
package wfp

import (
	"errors"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Spec describes the desired policy.
type Spec struct {
	// BlockDNS drops outbound DNS (port 53) on the listed adapters, so name
	// resolution can only leave through the tunnel.
	BlockDNS bool
	// LUIDs are the physical network adapters the rules apply to.
	LUIDs []uint64
	// KillApps are executable paths that must not use the listed adapters at
	// all (kill switch: the tunnel is the only way out for them).
	KillApps []string
}

var (
	mu      sync.Mutex
	current uintptr // handle of the active dynamic session
)

type wfpObjectInstaller func(uintptr) error

type base struct {
	provider windows.GUID
	filters  windows.GUID
}

func newSession() (uintptr, error) {
	dd, err := createWtFwpmDisplayData0("Hy2Tun", "Hy2Tun dynamic session")
	if err != nil {
		return 0, wrapErr(err)
	}
	s := wtFwpmSession0{
		displayData:          *dd,
		flags:                cFWPM_SESSION_FLAG_DYNAMIC,
		txnWaitTimeoutInMSec: windows.INFINITE,
	}
	var h uintptr
	if err := fwpmEngineOpen0(nil, cRPC_C_AUTHN_WINNT, nil, &s, unsafe.Pointer(&h)); err != nil {
		return 0, wrapErr(err)
	}
	return h, nil
}

func registerBase(session uintptr) (*base, error) {
	b := &base{}
	var err error
	if b.provider, err = windows.GenerateGUID(); err != nil {
		return nil, wrapErr(err)
	}
	if b.filters, err = windows.GenerateGUID(); err != nil {
		return nil, wrapErr(err)
	}
	dd, err := createWtFwpmDisplayData0("Hy2Tun", "Hy2Tun provider")
	if err != nil {
		return nil, wrapErr(err)
	}
	if err := fwpmProviderAdd0(session, &wtFwpmProvider0{providerKey: b.provider, displayData: *dd}, 0); err != nil {
		return nil, wrapErr(err)
	}
	dd2, err := createWtFwpmDisplayData0("Hy2Tun filters", "DNS leak protection and kill switch")
	if err != nil {
		return nil, wrapErr(err)
	}
	if err := fwpmSubLayerAdd0(session, &wtFwpmSublayer0{
		subLayerKey: b.filters, displayData: *dd2, providerKey: &b.provider, weight: ^uint16(0),
	}, 0); err != nil {
		return nil, wrapErr(err)
	}
	return b, nil
}

// addFilter installs one filter on both the IPv4 and IPv6 connect layers.
func addFilter(session uintptr, b *base, name string, conds []wtFwpmFilterCondition0, action wtFwpActionType, weight uint8) error {
	f := wtFwpmFilter0{
		providerKey: &b.provider,
		subLayerKey: b.filters,
		weight:      filterWeight(weight),
		action:      wtFwpmAction0{_type: action},
	}
	if len(conds) > 0 {
		f.numFilterConditions = uint32(len(conds))
		f.filterCondition = (*wtFwpmFilterCondition0)(unsafe.Pointer(&conds[0]))
	}
	for _, layer := range []struct {
		key  windows.GUID
		name string
	}{{cFWPM_LAYER_ALE_AUTH_CONNECT_V4, " (IPv4)"}, {cFWPM_LAYER_ALE_AUTH_CONNECT_V6, " (IPv6)"}} {
		dd, err := createWtFwpmDisplayData0("Hy2Tun: "+name+layer.name, "")
		if err != nil {
			return wrapErr(err)
		}
		f.displayData = *dd
		f.layerKey = layer.key
		var id uint64
		if err := fwpmFilterAdd0(session, &f, 0, &id); err != nil {
			return wrapErr(err)
		}
	}
	runtime.KeepAlive(conds)
	return nil
}

func condInterface(luid *uint64) wtFwpmFilterCondition0 {
	return wtFwpmFilterCondition0{
		fieldKey:  cFWPM_CONDITION_IP_LOCAL_INTERFACE,
		matchType: cFWP_MATCH_EQUAL,
		conditionValue: wtFwpConditionValue0{
			_type: cFWP_UINT64,
			value: uintptr(unsafe.Pointer(luid)),
		},
	}
}

func condAppID(appID *wtFwpByteBlob) wtFwpmFilterCondition0 {
	return wtFwpmFilterCondition0{
		fieldKey:  cFWPM_CONDITION_ALE_APP_ID,
		matchType: cFWP_MATCH_EQUAL,
		conditionValue: wtFwpConditionValue0{
			_type: cFWP_BYTE_BLOB_TYPE,
			value: uintptr(unsafe.Pointer(appID)),
		},
	}
}

func appIDFor(path string) (*wtFwpByteBlob, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	var id *wtFwpByteBlob
	if err := fwpmGetAppIdFromFileName0(p, unsafe.Pointer(&id)); err != nil {
		return nil, err
	}
	return id, nil
}

func install(session uintptr, s Spec) error {
	b, err := registerBase(session)
	if err != nil {
		return err
	}

	// 1. Our own process (service and GUI share the executable) is never
	//    restricted: its direct sockets carry the "direct" traffic and the
	//    DNS queries for RU names.
	self, err := getCurrentProcessAppID()
	if err != nil {
		return wrapErr(err)
	}
	defer fwpmFreeMemory0(unsafe.Pointer(&self))
	if err := addFilter(session, b, "permit Hy2Tun", []wtFwpmFilterCondition0{condAppID(self)}, cFWP_ACTION_PERMIT, 15); err != nil {
		return err
	}

	// 2. DNS may leave only through the tunnel.
	luids := append([]uint64(nil), s.LUIDs...)
	if s.BlockDNS {
		for i := range luids {
			conds := []wtFwpmFilterCondition0{
				condInterface(&luids[i]),
				{
					fieldKey:  cFWPM_CONDITION_IP_REMOTE_PORT,
					matchType: cFWP_MATCH_EQUAL,
					conditionValue: wtFwpConditionValue0{
						_type: cFWP_UINT16,
						value: uintptr(53),
					},
				},
			}
			if err := addFilter(session, b, "block DNS outside the tunnel", conds, cFWP_ACTION_BLOCK, 10); err != nil {
				return err
			}
		}
	}

	// 3. Kill switch: selected applications cannot use the physical adapters.
	for _, path := range s.KillApps {
		id, err := appIDFor(path)
		if err != nil {
			continue // file is gone / not resolvable: nothing to isolate
		}
		for i := range luids {
			conds := []wtFwpmFilterCondition0{condAppID(id), condInterface(&luids[i])}
			err := addFilter(session, b, "kill switch "+path, conds, cFWP_ACTION_BLOCK, 9)
			if err != nil {
				fwpmFreeMemory0(unsafe.Pointer(&id))
				return err
			}
		}
		fwpmFreeMemory0(unsafe.Pointer(&id))
	}
	runtime.KeepAlive(luids)
	return nil
}

// Apply replaces the active policy with s (an empty policy removes it).
// The new session is fully installed before the old one is closed, so there is
// no window without protection.
func Apply(s Spec) error {
	mu.Lock()
	defer mu.Unlock()
	if !s.BlockDNS && len(s.KillApps) == 0 {
		removeLocked()
		return nil
	}
	if len(s.LUIDs) == 0 {
		return errors.New("wfp: no adapters to protect")
	}
	session, err := newSession()
	if err != nil {
		return err
	}
	if err := runTransaction(session, func(h uintptr) error { return install(h, s) }); err != nil {
		fwpmEngineClose0(session)
		return err
	}
	old := current
	current = session
	if old != 0 {
		fwpmEngineClose0(old)
	}
	return nil
}

func removeLocked() {
	if current != 0 {
		fwpmEngineClose0(current)
		current = 0
	}
}

// Remove deletes every Hy2Tun filter.
func Remove() {
	mu.Lock()
	defer mu.Unlock()
	removeLocked()
}

// Active reports whether a policy is installed.
func Active() bool {
	mu.Lock()
	defer mu.Unlock()
	return current != 0
}
