package main

import (
	"log"
	"net/netip"
	"sync"
)

// flowRec is one live relayed connection together with the inputs of its
// routing decision, so it can be re-evaluated when the settings change.
type flowRec struct {
	proc    *procInfo
	name    string
	ip      netip.Addr
	via     via
	closeFn func()
}

// flowSet tracks live flows. Browsers keep HTTP/2 connections open for minutes;
// without re-evaluation a setting change would only affect *new* connections
// and the user would see no effect.
type flowSet struct {
	mu   sync.Mutex
	m    map[uint64]*flowRec
	next uint64
}

func newFlowSet() *flowSet { return &flowSet{m: make(map[uint64]*flowRec)} }

func (s *flowSet) add(r *flowRec) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	s.m[s.next] = r
	return s.next
}

func (s *flowSet) remove(id uint64) {
	s.mu.Lock()
	delete(s.m, id)
	s.mu.Unlock()
}

func (s *flowSet) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.m)
}

// reevaluate closes every live flow whose path would be different under the
// current settings; the application then reconnects and takes the new path.
func (t *tunnel) reevaluate() int {
	t.flows.mu.Lock()
	var stale []*flowRec
	for _, r := range t.flows.m {
		name := r.name
		if name == "" {
			name, _ = t.rt.dns.get(r.ip)
		}
		if v, _ := t.rt.decide(r.proc, name, r.ip); v != r.via {
			stale = append(stale, r)
		}
	}
	t.flows.mu.Unlock()
	for _, r := range stale {
		r.closeFn()
	}
	if len(stale) > 0 {
		log.Printf("settings changed: closed %d open connection(s) whose path changed", len(stale))
	}
	return len(stale)
}
