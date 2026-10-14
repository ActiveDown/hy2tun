package main

import (
	"encoding/binary"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modIphlpapi             = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetExtendedTCPTable = modIphlpapi.NewProc("GetExtendedTcpTable")
	procGetExtendedUDPTable = modIphlpapi.NewProc("GetExtendedUdpTable")
)

const (
	afInet              = 2
	afInet6             = 23
	tcpTableOwnerPidAll = 5
	udpTableOwnerPid    = 1
	errInsufficientBuf  = 122
)

// procRef identifies one process (or an ancestor of it).
type procRef struct {
	Name string // lower-case base name, e.g. "chrome.exe"
	Path string // lower-case full path when it could be read
}

// procInfo is the owner of a connection plus its ancestor chain (self first).
type procInfo struct {
	PID   uint32
	Chain []procRef
}

func (p *procInfo) name() string {
	if p == nil || len(p.Chain) == 0 {
		return ""
	}
	return p.Chain[0].Name
}

type tcpRow struct {
	lport, rport uint16
	raddr        netip.Addr
	pid          uint32
}

type udpRow struct {
	lport uint16
	v6    bool
	pid   uint32
}

type parentEntry struct {
	ppid uint32
	exe  string
}

type pathEntry struct {
	path string
	at   time.Time
}

// procTable resolves "which process owns this socket" using the Windows
// connection tables. Snapshots are cached very briefly; a miss forces a refresh.
type procTable struct {
	mu     sync.Mutex
	tcp    []tcpRow
	tcpAt  time.Time
	udp    []udpRow
	udpAt  time.Time
	parent map[uint32]parentEntry
	parAt  time.Time
	paths  map[uint32]pathEntry
}

func newProcTable() *procTable {
	return &procTable{paths: make(map[uint32]pathEntry)}
}

// port16 converts a port stored in network byte order in the low word.
func port16(dw uint32) uint16 {
	return uint16(dw&0xff)<<8 | uint16((dw>>8)&0xff)
}

func getTable(proc *windows.LazyProc, af, class uintptr) ([]byte, error) {
	size := uint32(32 << 10)
	for i := 0; i < 8; i++ {
		buf := make([]byte, size)
		r, _, _ := proc.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0, af, class, 0)
		if r == 0 {
			return buf[:size], nil
		}
		if r != errInsufficientBuf {
			return nil, syscall.Errno(r)
		}
		size += size / 2
	}
	return nil, syscall.Errno(errInsufficientBuf)
}

func readTCP() []tcpRow {
	var out []tcpRow
	if b, err := getTable(procGetExtendedTCPTable, afInet, tcpTableOwnerPidAll); err == nil && len(b) >= 4 {
		n := int(binary.LittleEndian.Uint32(b))
		for i := 0; i < n && 4+i*24+24 <= len(b); i++ {
			r := b[4+i*24:]
			ra, _ := netip.AddrFromSlice(r[12:16])
			out = append(out, tcpRow{
				lport: port16(binary.LittleEndian.Uint32(r[8:])),
				raddr: ra,
				rport: port16(binary.LittleEndian.Uint32(r[16:])),
				pid:   binary.LittleEndian.Uint32(r[20:]),
			})
		}
	}
	if b, err := getTable(procGetExtendedTCPTable, afInet6, tcpTableOwnerPidAll); err == nil && len(b) >= 4 {
		n := int(binary.LittleEndian.Uint32(b))
		for i := 0; i < n && 4+i*56+56 <= len(b); i++ {
			r := b[4+i*56:]
			// MIB_TCP6ROW_OWNER_PID: laddr[0:16] lscope[16] lport[20] raddr[24:40]
			// rscope[40] rport[44] state[48] pid[52]
			ra, _ := netip.AddrFromSlice(r[24:40])
			out = append(out, tcpRow{
				lport: port16(binary.LittleEndian.Uint32(r[20:])),
				raddr: ra.Unmap(),
				rport: port16(binary.LittleEndian.Uint32(r[44:])),
				pid:   binary.LittleEndian.Uint32(r[52:]),
			})
		}
	}
	return out
}

func readUDP() []udpRow {
	var out []udpRow
	if b, err := getTable(procGetExtendedUDPTable, afInet, udpTableOwnerPid); err == nil && len(b) >= 4 {
		n := int(binary.LittleEndian.Uint32(b))
		for i := 0; i < n && 4+i*12+12 <= len(b); i++ {
			r := b[4+i*12:]
			out = append(out, udpRow{lport: port16(binary.LittleEndian.Uint32(r[4:])), pid: binary.LittleEndian.Uint32(r[8:])})
		}
	}
	if b, err := getTable(procGetExtendedUDPTable, afInet6, udpTableOwnerPid); err == nil && len(b) >= 4 {
		n := int(binary.LittleEndian.Uint32(b))
		for i := 0; i < n && 4+i*28+28 <= len(b); i++ {
			r := b[4+i*28:]
			out = append(out, udpRow{lport: port16(binary.LittleEndian.Uint32(r[20:])), v6: true, pid: binary.LittleEndian.Uint32(r[24:])})
		}
	}
	return out
}

// refreshParentsLocked rebuilds the pid -> (parent, exe) map; mu must be held.
func (pt *procTable) refreshParentsLocked() {
	if pt.parent != nil && time.Since(pt.parAt) < 2*time.Second {
		return
	}
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return
	}
	defer windows.CloseHandle(snap)
	m := make(map[uint32]parentEntry, 512)
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	if windows.Process32First(snap, &pe) == nil {
		for {
			m[pe.ProcessID] = parentEntry{ppid: pe.ParentProcessID, exe: strings.ToLower(windows.UTF16ToString(pe.ExeFile[:]))}
			if windows.Process32Next(snap, &pe) != nil {
				break
			}
		}
	}
	pt.parent, pt.parAt = m, time.Now()
}

// pathOfLocked returns the full image path of pid (cached); mu must be held.
func (pt *procTable) pathOfLocked(pid uint32) string {
	if e, ok := pt.paths[pid]; ok && time.Since(e.at) < time.Minute {
		return e.path
	}
	var path string
	if h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid); err == nil {
		buf := make([]uint16, 1024)
		size := uint32(len(buf))
		if windows.QueryFullProcessImageName(h, 0, &buf[0], &size) == nil {
			path = strings.ToLower(windows.UTF16ToString(buf[:size]))
		}
		windows.CloseHandle(h)
	}
	pt.paths[pid] = pathEntry{path: path, at: time.Now()}
	return path
}

// chain returns [process, parent, grandparent, ...] (at most 8 levels).
func (pt *procTable) chain(pid uint32) []procRef {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	pt.refreshParentsLocked()
	var out []procRef
	seen := map[uint32]bool{}
	for i := 0; i < 8 && pid != 0 && !seen[pid]; i++ {
		seen[pid] = true
		e, ok := pt.parent[pid]
		path := pt.pathOfLocked(pid)
		name := e.exe
		if path != "" {
			name = strings.ToLower(filepath.Base(path))
		}
		if name == "" && !ok {
			break
		}
		out = append(out, procRef{Name: name, Path: path})
		pid = e.ppid
	}
	return out
}

func (pt *procTable) scanTCP(srcPort uint16, dst netip.Addr, dstPort uint16, retry bool) (uint32, bool) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	age := time.Since(pt.tcpAt)
	if (!retry && age > 150*time.Millisecond) || (retry && age > 20*time.Millisecond) {
		pt.tcp, pt.tcpAt = readTCP(), time.Now()
	}
	for _, r := range pt.tcp {
		if r.lport == srcPort && r.rport == dstPort && r.raddr == dst && r.pid != 0 {
			return r.pid, true
		}
	}
	return 0, false
}

// lookupTCP finds the owner of an outgoing TCP connection (src port -> dst).
func (pt *procTable) lookupTCP(srcPort uint16, dst netip.Addr, dstPort uint16) *procInfo {
	dst = dst.Unmap()
	for attempt := 0; attempt < 4; attempt++ {
		if pid, ok := pt.scanTCP(srcPort, dst, dstPort, attempt > 0); ok {
			return &procInfo{PID: pid, Chain: pt.chain(pid)}
		}
		time.Sleep(15 * time.Millisecond)
	}
	return nil
}

func (pt *procTable) scanUDP(srcPort uint16, v6, retry bool) (uint32, bool) {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	age := time.Since(pt.udpAt)
	if (!retry && age > 150*time.Millisecond) || (retry && age > 20*time.Millisecond) {
		pt.udp, pt.udpAt = readUDP(), time.Now()
	}
	for _, r := range pt.udp {
		if r.lport == srcPort && r.v6 == v6 && r.pid != 0 {
			return r.pid, true
		}
	}
	return 0, false
}

// lookupUDP finds the owner of a UDP socket by its local port.
func (pt *procTable) lookupUDP(srcPort uint16, v6 bool) *procInfo {
	for attempt := 0; attempt < 3; attempt++ {
		if pid, ok := pt.scanUDP(srcPort, v6, attempt > 0); ok {
			return &procInfo{PID: pid, Chain: pt.chain(pid)}
		}
		time.Sleep(15 * time.Millisecond)
	}
	return nil
}

// runningProc is one entry of the "pick an app" list in the panel.
type runningProc struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Conns int    `json:"conns"`
}

// runningWithNetwork lists processes that currently own sockets.
func (pt *procTable) runningWithNetwork() []runningProc {
	counts := map[uint32]int{}
	for _, r := range readTCP() {
		counts[r.pid]++
	}
	for _, r := range readUDP() {
		counts[r.pid]++
	}
	pt.mu.Lock()
	defer pt.mu.Unlock()
	pt.refreshParentsLocked()
	agg := map[string]*runningProc{}
	for pid, n := range counts {
		if pid == 0 || pid == 4 {
			continue
		}
		path := pt.pathOfLocked(pid)
		name := pt.parent[pid].exe
		if path != "" {
			name = strings.ToLower(filepath.Base(path))
		}
		if name == "" {
			continue
		}
		if a, ok := agg[name]; ok {
			a.Conns += n
		} else {
			agg[name] = &runningProc{Name: name, Path: path, Conns: n}
		}
	}
	out := make([]runningProc, 0, len(agg))
	for _, a := range agg {
		out = append(out, *a)
	}
	return out
}

// pathsByName finds the executable paths of running processes whose name is in
// names (lower-case). Used by the kill switch.
func (pt *procTable) pathsByName(names map[string]struct{}) map[string][]string {
	out := map[string][]string{}
	if len(names) == 0 {
		return out
	}
	pt.mu.Lock()
	defer pt.mu.Unlock()
	pt.refreshParentsLocked()
	for pid, e := range pt.parent {
		if _, ok := names[e.exe]; !ok {
			continue
		}
		if path := pt.pathOfLocked(pid); path != "" {
			dup := false
			for _, p := range out[e.exe] {
				if p == path {
					dup = true
				}
			}
			if !dup {
				out[e.exe] = append(out[e.exe], path)
			}
		}
	}
	return out
}
