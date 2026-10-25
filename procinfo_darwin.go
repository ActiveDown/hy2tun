//go:build darwin

package main

// #include <libproc.h>
// #include <unistd.h>
import "C"

import (
	"bufio"
	"bytes"
	"net/netip"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"
)

type tcpEntry struct {
	lport uint16
	raddr netip.Addr
	rport uint16
	pid   uint32
}

type udpEntry struct {
	lport uint16
	v6    bool
	pid   uint32
}

type pathEntry struct {
	path string
	at   time.Time
}

type procTable struct {
	mu    sync.Mutex
	tcp   []tcpEntry
	tcpAt time.Time
	udp   []udpEntry
	udpAt time.Time
	paths map[uint32]pathEntry
}

func newProcTable() *procTable {
	return &procTable{
		paths: make(map[uint32]pathEntry),
	}
}

func pidPath(pid uint32) string {
	if pid == 0 {
		return ""
	}
	var buf [C.PROC_PIDPATHINFO_MAXSIZE]C.char
	ret := C.proc_pidpath(C.int(pid), unsafe.Pointer(&buf[0]), C.uint32_t(len(buf)))
	if ret > 0 {
		return C.GoString(&buf[0])
	}
	return ""
}

func pidInfo(pid uint32) (ppid uint32, name string) {
	if pid == 0 {
		return 0, ""
	}
	var bsd C.struct_proc_bsdinfo
	ret := C.proc_pidinfo(C.int(pid), C.PROC_PIDTBSDINFO, 0, unsafe.Pointer(&bsd), C.int(unsafe.Sizeof(bsd)))
	if ret > 0 {
		return uint32(bsd.pbi_ppid), C.GoString(&bsd.pbi_name[0])
	}
	return 0, ""
}

func (pt *procTable) pathOfLocked(pid uint32) string {
	if e, ok := pt.paths[pid]; ok && time.Since(e.at) < time.Minute {
		return e.path
	}
	path := strings.ToLower(pidPath(pid))
	pt.paths[pid] = pathEntry{path: path, at: time.Now()}
	return path
}

func (pt *procTable) chain(pid uint32) []procRef {
	pt.mu.Lock()
	defer pt.mu.Unlock()

	var out []procRef
	seen := map[uint32]bool{}

	for i := 0; i < 8 && pid != 0 && !seen[pid]; i++ {
		seen[pid] = true
		ppid, procName := pidInfo(pid)
		path := pt.pathOfLocked(pid)
		name := strings.ToLower(procName)
		if path != "" {
			name = strings.ToLower(filepath.Base(path))
		}
		if name == "" && ppid == 0 {
			break
		}
		if name != "" || path != "" {
			out = append(out, procRef{Name: name, Path: path})
		}
		pid = ppid
	}
	return out
}

// refreshSocketsLocked takes a socket snapshot unless a recent one exists
// (force ignores its age). pt.tcpAt is when the snapshot was started.
func (pt *procTable) refreshSocketsLocked() { pt.snapshotLocked(false) }

func (pt *procTable) snapshotLocked(force bool) {
	now := time.Now()
	if !force && time.Since(pt.tcpAt) < 150*time.Millisecond {
		return
	}

	cmd := exec.Command("lsof", "-n", "-P", "-i", "-F", "pcn")
	out, err := cmd.Output()
	if err != nil {
		return
	}

	var curPID uint32
	var tcps []tcpEntry
	var udps []udpEntry

	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) < 2 {
			continue
		}
		tag, val := line[0], line[1:]
		switch tag {
		case 'p':
			if p, err := strconv.ParseUint(val, 10, 32); err == nil {
				curPID = uint32(p)
			}
		case 'n':
			if curPID == 0 {
				continue
			}
			// Examples:
			// 192.168.0.127:49251->95.142.44.214:8772 (TCP established)
			// *:8770 (Listen or UDP)
			// 127.0.0.1:10391
			// [::1]:53
			if strings.Contains(val, "->") {
				parts := strings.Split(val, "->")
				if len(parts) == 2 {
					lAddr := parts[0]
					rAddr := parts[1]

					lPort := parsePort(lAddr)
					if ra, rPort, err := parseAddrPort(rAddr); err == nil {
						tcps = append(tcps, tcpEntry{
							lport: lPort,
							raddr: ra,
							rport: rPort,
							pid:   curPID,
						})
					}
				}
			} else {
				port := parsePort(val)
				if port > 0 {
					udps = append(udps, udpEntry{
						lport: port,
						v6:    strings.Contains(val, "[") || strings.Contains(val, ":"),
						pid:   curPID,
					})
				}
			}
		}
	}

	pt.tcp = tcps
	pt.udp = udps
	pt.tcpAt = now
	pt.udpAt = now
}

func parsePort(s string) uint16 {
	idx := strings.LastIndex(s, ":")
	if idx == -1 || idx+1 >= len(s) {
		return 0
	}
	p, _ := strconv.ParseUint(s[idx+1:], 10, 16)
	return uint16(p)
}

func parseAddrPort(s string) (netip.Addr, uint16, error) {
	idx := strings.LastIndex(s, ":")
	if idx == -1 || idx+1 >= len(s) {
		return netip.Addr{}, 0, strconv.ErrSyntax
	}
	host := s[:idx]
	host = strings.TrimPrefix(host, "[")
	host = strings.TrimSuffix(host, "]")
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, 0, err
	}
	p, err := strconv.ParseUint(s[idx+1:], 10, 16)
	if err != nil {
		return netip.Addr{}, 0, err
	}
	return addr, uint16(p), nil
}

func (pt *procTable) findTCPLocked(srcPort uint16, dst netip.Addr, dstPort uint16) (uint32, bool) {
	for _, r := range pt.tcp {
		if r.lport == srcPort && (r.rport == dstPort || dstPort == 0) && (r.raddr == dst || !dst.IsValid()) {
			return r.pid, true
		}
	}
	// Fallback check by local port only if remote didn't match immediately
	for _, r := range pt.tcp {
		if r.lport == srcPort {
			return r.pid, true
		}
	}
	return 0, false
}

func (pt *procTable) lookupTCP(srcPort uint16, dst netip.Addr, dstPort uint16) *procInfo {
	dst = dst.Unmap()
	asked := time.Now()
	pt.mu.Lock()
	pt.refreshSocketsLocked()
	pid, ok := pt.findTCPLocked(srcPort, dst, dstPort)
	if !ok && pt.tcpAt.Before(asked) {
		// The snapshot is older than this flow: its socket may be missing from it.
		pt.snapshotLocked(true)
		pid, ok = pt.findTCPLocked(srcPort, dst, dstPort)
	}
	pt.mu.Unlock()
	if !ok {
		return nil
	}
	return &procInfo{PID: pid, Chain: pt.chain(pid)}
}

func (pt *procTable) findUDPLocked(srcPort uint16) (uint32, bool) {
	for _, r := range pt.udp {
		if r.lport == srcPort {
			return r.pid, true
		}
	}
	return 0, false
}

func (pt *procTable) lookupUDP(srcPort uint16, v6 bool) *procInfo {
	asked := time.Now()
	pt.mu.Lock()
	pt.refreshSocketsLocked()
	pid, ok := pt.findUDPLocked(srcPort)
	if !ok && pt.tcpAt.Before(asked) {
		pt.snapshotLocked(true)
		pid, ok = pt.findUDPLocked(srcPort)
	}
	pt.mu.Unlock()
	if !ok {
		return nil
	}
	return &procInfo{PID: pid, Chain: pt.chain(pid)}
}

func (pt *procTable) runningWithNetwork() []runningProc {
	pt.mu.Lock()
	pt.refreshSocketsLocked()
	counts := make(map[uint32]int)
	for _, r := range pt.tcp {
		counts[r.pid]++
	}
	for _, r := range pt.udp {
		counts[r.pid]++
	}
	pt.mu.Unlock()

	agg := make(map[string]*runningProc)
	for pid, n := range counts {
		if pid == 0 {
			continue
		}
		path := pidPath(pid)
		_, rawName := pidInfo(pid)
		name := strings.ToLower(rawName)
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

func (pt *procTable) pathsByName(names map[string]struct{}) map[string][]string {
	out := make(map[string][]string)
	if len(names) == 0 {
		return out
	}

	pt.mu.Lock()
	pt.refreshSocketsLocked()
	var pids []uint32
	seen := map[uint32]bool{}
	for _, r := range pt.tcp {
		if !seen[r.pid] {
			seen[r.pid] = true
			pids = append(pids, r.pid)
		}
	}
	for _, r := range pt.udp {
		if !seen[r.pid] {
			seen[r.pid] = true
			pids = append(pids, r.pid)
		}
	}
	pt.mu.Unlock()

	for _, pid := range pids {
		_, rawName := pidInfo(pid)
		exe := strings.ToLower(rawName)
		if _, ok := names[exe]; !ok {
			path := pidPath(pid)
			base := strings.ToLower(filepath.Base(path))
			if _, ok2 := names[base]; !ok2 {
				continue
			}
			exe = base
		}
		if path := pidPath(pid); path != "" {
			dup := false
			for _, p := range out[exe] {
				if p == path {
					dup = true
					break
				}
			}
			if !dup {
				out[exe] = append(out[exe], path)
			}
		}
	}

	return out
}
