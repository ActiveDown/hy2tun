package main

import (
	"context"
	"io"
	"log"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/apernet/hysteria/core/v2/client"
	"golang.org/x/net/dns/dnsmessage"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
	"gvisor.dev/gvisor/pkg/waiter"
)

func ipString(a tcpip.Address) string { return net.IP(a.AsSlice()).String() }

func addrOf(a tcpip.Address) netip.Addr {
	ip, _ := netip.AddrFromSlice(a.AsSlice())
	return ip.Unmap()
}

var copyBufs = sync.Pool{New: func() any { b := make([]byte, 32<<10); return &b }}

func (t *tunnel) copyOne(dst io.Writer, src io.Reader, counter *atomic.Int64) {
	bp := copyBufs.Get().(*[]byte)
	defer copyBufs.Put(bp)
	n, _ := io.CopyBuffer(dst, src, *bp)
	counter.Add(n)
}

// tunSubnet is the TUN adapter's own /30; nothing useful lives there
// (NetBIOS / Steam LAN discovery broadcasts end up on .3).
var tunSubnet = netip.MustParsePrefix(tunV4Addr + "/30")

func isNonUnicast(a tcpip.Address) bool {
	ip := net.IP(a.AsSlice())
	if v, ok := netip.AddrFromSlice(a.AsSlice()); ok && tunSubnet.Contains(v.Unmap()) {
		return true
	}
	return ip.IsMulticast() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.Equal(net.IPv4bcast)
}

// directUnavailable reports whether a flow to ip is known (from DNS) to be
// meant for the direct path although the physical network cannot carry it
// (an IPv6 destination while the real adapter has no IPv6).
func (t *tunnel) directUnavailable(p *procInfo, ip netip.Addr) bool {
	if !ip.Is6() || t.rt.physV6 || !t.rt.hasDirect {
		return false
	}
	name, _ := t.rt.dns.get(ip)
	v, _ := t.rt.decide(p, name, ip)
	return v == viaDirect
}

// procName is the display name of a flow's owner for logs and the panel.
func procName(p *procInfo) string {
	if n := p.name(); n != "" {
		return n
	}
	return "?"
}

// nameFor returns the best known domain for a flow: SNI/Host if we peeked it,
// otherwise whatever DNS told us about the destination IP.
func (t *tunnel) nameFor(ip netip.Addr, sniName string) string {
	if sniName != "" {
		t.rt.dns.put(ip, sniName, 10*time.Minute)
		return sniName
	}
	n, _ := t.rt.dns.get(ip)
	return n
}

func (t *tunnel) handleTCP(req *tcp.ForwarderRequest) {
	id := req.ID()
	ip := addrOf(id.LocalAddress)
	dst := net.JoinHostPort(ipString(id.LocalAddress), strconv.Itoa(int(id.LocalPort)))

	// Refuse before the handshake completes, so the application gets an
	// immediate error and falls back (e.g. IPv6 -> IPv4) instead of seeing a
	// connection that opens and then dies.
	if isNonUnicast(id.LocalAddress) {
		req.Complete(true)
		return
	}
	pi := t.procs.lookupTCP(id.RemotePort, ip, id.LocalPort)
	if t.directUnavailable(pi, ip) {
		if t.verbose {
			log.Printf("tcp %s [%s] refused (no direct IPv6 path)", dst, procName(pi))
		}
		req.Complete(true)
		return
	}

	var wq waiter.Queue
	ep, terr := req.CreateEndpoint(&wq)
	if terr != nil {
		req.Complete(true)
		return
	}
	req.Complete(false)
	local := gonet.NewTCPConn(&wq, ep)
	t.tcpFlows.Add(1)

	go func() {
		defer local.Close()

		var sni string
		var first []byte
		switch id.LocalPort {
		case 443, 8443, 80, 8080:
			sni, first = peekName(local, 400*time.Millisecond)
		}
		name := t.nameFor(ip, sni)
		path, why := t.rt.decide(pi, name, ip)
		pn := procName(pi)
		t.rt.record(pn, name, dst, path, why)

		var remote net.Conn
		var err error
		if path == viaDirect {
			ctx, cancel := context.WithTimeout(t.ctx, 10*time.Second)
			remote, err = t.direct.dialTCP(ctx, dst)
			cancel()
		} else {
			remote, err = t.hc.TCP(dst)
		}
		if err != nil {
			t.tcpFail.Add(1)
			if t.verbose {
				log.Printf("tcp %s [%s] {%s} %s: %v", dst, name, pn, path, err)
			}
			return
		}
		defer remote.Close()
		if t.verbose {
			log.Printf("tcp %s [%s] {%s} -> %s (%s)", dst, name, pn, path, why)
		}
		fid := t.flows.add(&flowRec{proc: pi, name: name, ip: ip, via: path,
			closeFn: func() { local.Close(); remote.Close() }})
		defer t.flows.remove(fid)
		if len(first) > 0 {
			if _, err := remote.Write(first); err != nil {
				return
			}
			t.bytesUp.Add(int64(len(first)))
		}
		done := make(chan struct{}, 2)
		go func() { t.copyOne(remote, local, &t.bytesUp); done <- struct{}{} }()
		go func() { t.copyOne(local, remote, &t.bytesDown); done <- struct{}{} }()
		<-done
	}()
}

// udpPath is the upstream side of one UDP flow.
type udpPath interface {
	Send(p []byte) error
	Receive() ([]byte, error)
	Close() error
}

type hyPath struct {
	c   client.HyUDPConn
	dst string
}

func (h *hyPath) Send(p []byte) error      { return h.c.Send(p, h.dst) }
func (h *hyPath) Receive() ([]byte, error) { d, _, err := h.c.Receive(); return d, err }
func (h *hyPath) Close() error             { return h.c.Close() }

type directPath struct{ c net.Conn }

func (d *directPath) Send(p []byte) error { _, err := d.c.Write(p); return err }
func (d *directPath) Receive() ([]byte, error) {
	buf := make([]byte, 65535)
	n, err := d.c.Read(buf)
	return buf[:n], err
}
func (d *directPath) Close() error { return d.c.Close() }

// dnsQuestion extracts the first question of a DNS message.
func dnsQuestion(msg []byte) (name string, qtype dnsmessage.Type) {
	var p dnsmessage.Parser
	if _, err := p.Start(msg); err != nil {
		return "", 0
	}
	q, err := p.Question()
	if err != nil {
		return "", 0
	}
	return q.Name.String(), q.Type
}

// emptyDNSResponse builds a NOERROR answer without records (NODATA) for q.
// Used for AAAA queries of direct names when the physical network has no
// IPv6, so applications never try an IPv6 address that cannot work.
func emptyDNSResponse(q []byte) []byte {
	var p dnsmessage.Parser
	h, err := p.Start(q)
	if err != nil {
		return nil
	}
	question, err := p.Question()
	if err != nil {
		return nil
	}
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID: h.ID, Response: true, RecursionDesired: h.RecursionDesired, RecursionAvailable: true,
		RCode: dnsmessage.RCodeSuccess,
	})
	if b.StartQuestions() != nil || b.Question(question) != nil {
		return nil
	}
	out, err := b.Finish()
	if err != nil {
		return nil
	}
	return out
}

// learnDNS records IP -> queried name from a DNS response.
func (t *tunnel) learnDNS(msg []byte) {
	var p dnsmessage.Parser
	h, err := p.Start(msg)
	if err != nil || !h.Response {
		return
	}
	q, err := p.Question()
	if err != nil {
		return
	}
	name := q.Name.String()
	p.SkipAllQuestions()
	for {
		rh, err := p.AnswerHeader()
		if err != nil {
			return
		}
		ttl := time.Duration(rh.TTL) * time.Second
		switch rh.Type {
		case dnsmessage.TypeA:
			r, err := p.AResource()
			if err != nil {
				return
			}
			t.rt.dns.put(netip.AddrFrom4(r.A), name, ttl)
		case dnsmessage.TypeAAAA:
			r, err := p.AAAAResource()
			if err != nil {
				return
			}
			t.rt.dns.put(netip.AddrFrom16(r.AAAA), name, ttl)
		default:
			if p.SkipAnswer() != nil {
				return
			}
		}
	}
}

// directDNS answers a query for an RU name through a RU resolver on the
// physical interface, which gives geo-correct answers.
func (t *tunnel) directDNS(local net.Conn, q []byte) {
	ctx, cancel := context.WithTimeout(t.ctx, 4*time.Second)
	defer cancel()
	c, err := t.direct.dialUDP(ctx, t.directDNSAddr)
	if err != nil {
		return
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(4 * time.Second))
	if _, err := c.Write(q); err != nil {
		return
	}
	buf := make([]byte, 4096)
	n, err := c.Read(buf)
	if err != nil {
		return
	}
	t.learnDNS(buf[:n])
	local.Write(buf[:n])
}

func (t *tunnel) handleUDP(req *udp.ForwarderRequest) {
	id := req.ID()
	if isNonUnicast(id.LocalAddress) {
		return
	}
	if t.noQUIC && id.LocalPort == 443 {
		return // dropped: browsers fall back from QUIC to TCP
	}
	ip := addrOf(id.LocalAddress)
	dst := net.JoinHostPort(ipString(id.LocalAddress), strconv.Itoa(int(id.LocalPort)))
	isDNS := id.LocalPort == 53

	var wq waiter.Queue
	ep, terr := req.CreateEndpoint(&wq)
	if terr != nil {
		return
	}
	local := gonet.NewUDPConn(&wq, ep)
	t.udpFlows.Add(1)

	idle := 90 * time.Second
	if isDNS {
		idle = 15 * time.Second
	}

	go func() {
		defer local.Close()

		var up udpPath
		var name, pn, why string
		var pi *procInfo
		path := viaTunnel
		if !isDNS {
			pi = t.procs.lookupUDP(id.RemotePort, len(id.RemoteAddress.AsSlice()) == 16)
			name = t.nameFor(ip, "")
			pn = procName(pi)
			path, why = t.rt.decide(pi, name, ip)
			t.rt.record(pn, name, dst, path, why)
		}
		if path == viaDirect {
			ctx, cancel := context.WithTimeout(t.ctx, 5*time.Second)
			c, err := t.direct.dialUDP(ctx, dst)
			cancel()
			if err != nil {
				return
			}
			up = &directPath{c: c}
		} else {
			hu, err := t.hc.UDP()
			if err != nil {
				if t.verbose {
					log.Printf("udp %s: %v", dst, err)
				}
				return
			}
			up = &hyPath{c: hu, dst: dst}
		}
		defer up.Close()
		if t.verbose && !isDNS {
			log.Printf("udp %s [%s] {%s} -> %s (%s)", dst, name, pn, path, why)
		}

		stop := make(chan struct{})
		var once sync.Once
		finish := func() { once.Do(func() { close(stop) }) }
		if !isDNS {
			fid := t.flows.add(&flowRec{proc: pi, name: name, ip: ip, via: path, closeFn: finish})
			defer t.flows.remove(fid)
		}

		go func() { // app -> upstream
			defer finish()
			buf := make([]byte, 65535)
			for {
				local.SetReadDeadline(time.Now().Add(idle))
				n, err := local.Read(buf)
				if err != nil {
					return
				}
				pkt := append([]byte(nil), buf[:n]...)
				if isDNS {
					qn, qt := dnsQuestion(pkt)
					p := t.rt.decideDNS(qn)
					t.rt.record("dns", qn, dst, p, "DNS")
					if p == viaDirect {
						if qt == dnsmessage.TypeAAAA && !t.rt.physV6 {
							if resp := emptyDNSResponse(pkt); resp != nil {
								local.Write(resp)
								continue
							}
						}
						go t.directDNS(local, pkt)
						continue
					}
				}
				if err := up.Send(pkt); err != nil {
					return
				}
				t.bytesUp.Add(int64(n))
			}
		}()
		go func() { // upstream -> app
			defer finish()
			for {
				data, err := up.Receive()
				if err != nil {
					return
				}
				if isDNS {
					t.learnDNS(data)
				}
				if _, err := local.Write(data); err != nil {
					return
				}
				t.bytesDown.Add(int64(len(data)))
			}
		}()
		<-stop
	}()
}
