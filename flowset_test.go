package main

import (
	"net/netip"
	"os"
	"sync/atomic"
	"testing"
)

func TestReevaluateClosesOnlyChangedFlows(t *testing.T) {
	tn := &tunnel{rt: newRouter(os.DevNull, true), flows: newFlowSet()}
	c := tn.rt.config()
	c.ProxyRU = true // .ru goes through the tunnel
	tn.rt.setConfig(c)

	var closedRU, closedOther, closedDirect atomic.Int32
	ip := netip.MustParseAddr("203.0.113.5")
	tn.flows.add(&flowRec{name: "yandex.ru", ip: ip, via: viaTunnel, closeFn: func() { closedRU.Add(1) }})
	tn.flows.add(&flowRec{name: "google.com", ip: ip, via: viaTunnel, closeFn: func() { closedOther.Add(1) }})
	tn.flows.add(&flowRec{name: "mail.ru", ip: ip, via: viaDirect, closeFn: func() { closedDirect.Add(1) }})

	if n := tn.reevaluate(); n != 1 || closedDirect.Load() != 1 {
		t.Fatalf("with proxy-ru on only the direct .ru flow is stale: n=%d direct=%d", n, closedDirect.Load())
	}

	c.ProxyRU = false // now .ru must go direct
	tn.rt.setConfig(c)
	closedDirect.Store(0)
	if n := tn.reevaluate(); n != 1 {
		t.Fatalf("only the yandex.ru flow on the tunnel became stale, n=%d", n)
	}
	if closedRU.Load() != 1 {
		t.Fatal("the yandex.ru flow on the tunnel must be closed so it reconnects directly")
	}
	if closedDirect.Load() != 0 {
		t.Fatal("mail.ru is already direct and must stay open")
	}
	if closedOther.Load() != 0 {
		t.Fatal("unrelated flows must stay open")
	}
}

func TestFlowSetAddRemove(t *testing.T) {
	s := newFlowSet()
	id := s.add(&flowRec{})
	if s.len() != 1 {
		t.Fatal("add failed")
	}
	s.remove(id)
	if s.len() != 0 {
		t.Fatal("remove failed")
	}
}
