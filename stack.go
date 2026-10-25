package main

import (
	"context"
	"fmt"
	"log"
	"sync/atomic"

	"github.com/apernet/hysteria/core/v2/client"
	"golang.zx2c4.com/wireguard/tun"
	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/icmp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
)

const nicID = 1

// tunnel glues a Wintun device to a userspace TCP/IP stack and relays every
// flow it sees through a Hysteria2 client.
type tunnel struct {
	dev           tun.Device
	hc            client.Client
	ep            *channel.Endpoint
	stk           *stack.Stack
	ctx           context.Context
	cancel        context.CancelFunc
	verbose       bool
	noQUIC        bool
	rt            *router
	direct        *directDialer
	procs         *procTable
	flows         *flowSet
	directDNSAddr string

	tcpFlows, udpFlows atomic.Int64
	tcpFail            atomic.Int64
	bytesUp, bytesDown atomic.Int64
}

func newTunnel(dev tun.Device, hc client.Client, rt *router, mtu int, verbose, noQUIC bool) (*tunnel, error) {
	t := &tunnel{dev: dev, hc: hc, rt: rt, procs: newProcTable(), flows: newFlowSet(), verbose: verbose, noQUIC: noQUIC}
	t.ctx, t.cancel = context.WithCancel(context.Background())

	t.stk = stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, ipv6.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol, icmp.NewProtocol4, icmp.NewProtocol6},
	})
	t.ep = channel.New(2048, uint32(mtu), "")

	if e := t.stk.CreateNIC(nicID, t.ep); e != nil {
		return nil, fmt.Errorf("CreateNIC: %v", e)
	}
	// Accept packets for any destination and answer as that destination.
	if e := t.stk.SetPromiscuousMode(nicID, true); e != nil {
		return nil, fmt.Errorf("SetPromiscuousMode: %v", e)
	}
	if e := t.stk.SetSpoofing(nicID, true); e != nil {
		return nil, fmt.Errorf("SetSpoofing: %v", e)
	}
	t.stk.SetRouteTable([]tcpip.Route{
		{Destination: header.IPv4EmptySubnet, NIC: nicID},
		{Destination: header.IPv6EmptySubnet, NIC: nicID},
	})

	sack := tcpip.TCPSACKEnabled(true)
	t.stk.SetTransportProtocolOption(tcp.ProtocolNumber, &sack)
	rcv := tcpip.TCPReceiveBufferSizeRangeOption{Min: 4 << 10, Default: 1 << 20, Max: 8 << 20}
	t.stk.SetTransportProtocolOption(tcp.ProtocolNumber, &rcv)
	snd := tcpip.TCPSendBufferSizeRangeOption{Min: 4 << 10, Default: 1 << 20, Max: 8 << 20}
	t.stk.SetTransportProtocolOption(tcp.ProtocolNumber, &snd)
	mod := tcpip.TCPModerateReceiveBufferOption(true)
	t.stk.SetTransportProtocolOption(tcp.ProtocolNumber, &mod)

	tcpFwd := tcp.NewForwarder(t.stk, 0, 4096, t.handleTCP)
	t.stk.SetTransportProtocolHandler(tcp.ProtocolNumber, tcpFwd.HandlePacket)
	udpFwd := udp.NewForwarder(t.stk, t.handleUDP)
	t.stk.SetTransportProtocolHandler(udp.ProtocolNumber, udpFwd.HandlePacket)
	return t, nil
}

// start runs the two packet pumps (TUN -> stack, stack -> TUN).
func (t *tunnel) start() {
	go t.pumpIn()
	go t.pumpOut()
}

func (t *tunnel) pumpIn() {
	bs := t.dev.BatchSize()
	bufs := make([][]byte, bs)
	sizes := make([]int, bs)
	for i := range bufs {
		bufs[i] = make([]byte, tunOffset+65535)
	}
	for {
		n, err := t.dev.Read(bufs, sizes, tunOffset)
		if err != nil {
			if t.ctx.Err() == nil {
				log.Printf("tun read: %v", err)
			}
			return
		}
		for i := 0; i < n; i++ {
			p := bufs[i][tunOffset : tunOffset+sizes[i]]
			if len(p) == 0 {
				continue
			}
			pkb := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(p)})
			switch p[0] >> 4 {
			case 4:
				t.ep.InjectInbound(header.IPv4ProtocolNumber, pkb)
			case 6:
				t.ep.InjectInbound(header.IPv6ProtocolNumber, pkb)
			}
			pkb.DecRef()
		}
	}
}

func (t *tunnel) pumpOut() {
	for {
		pkt := t.ep.ReadContext(t.ctx)
		if pkt == nil {
			return
		}
		view := pkt.ToView()
		pkt.DecRef()
		b := view.AsSlice()
		if len(b) > 0 {
			if tunOffset > 0 {
				// the macOS utun driver needs room for its 4-byte AF header before the packet
				framed := make([]byte, tunOffset+len(b))
				copy(framed[tunOffset:], b)
				b = framed
			}
			if _, err := t.dev.Write([][]byte{b}, tunOffset); err != nil && t.ctx.Err() == nil {
				log.Printf("tun write: %v", err)
			}
		}
		view.Release()
	}
}

func (t *tunnel) close() {
	t.cancel()
	t.stk.RemoveNIC(nicID)
	t.stk.Close()
	t.ep.Close()
}
