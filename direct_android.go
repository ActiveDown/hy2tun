package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// tunOffset: the VpnService descriptor carries bare IP packets.
const tunOffset = 0

// protectWarned limits the "protect failed" warning to one line.
var protectWarned atomic.Bool

// protectControl exempts a socket from the VPN (VpnService.protect), so the
// server connection and direct flows can never loop back into the tunnel. The
// app is also excluded from its own VPN, so a failed protect is not fatal.
func protectControl(network, address string, c syscall.RawConn) error {
	return c.Control(func(fd uintptr) {
		if !protectFd(int(fd)) && !protectWarned.Swap(true) {
			log.Printf("warn: VpnService.protect failed for a %s socket", network)
		}
	})
}

func init() {
	ownUDPSocket = true
	listenUDP = func() (net.PacketConn, error) {
		lc := net.ListenConfig{Control: protectControl}
		return lc.ListenPacket(context.Background(), "udp", ":0")
	}
}

// directDialer opens protected sockets: they leave over the underlying network.
type directDialer struct {
	d net.Dialer
}

func parseIfIndex(s string) (uint32, error) {
	n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 32)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("bad interface index %q", s)
	}
	return uint32(n), nil
}

func newDirectDialer(ifIndex string) (*directDialer, error) {
	return &directDialer{d: net.Dialer{Timeout: 10 * time.Second, Control: protectControl}}, nil
}

// setIfIndex: the system picks the underlying network itself.
func (dd *directDialer) setIfIndex(idx uint32) {}

func (dd *directDialer) dialTCP(ctx context.Context, addr string) (net.Conn, error) {
	return dd.d.DialContext(ctx, "tcp", addr)
}

func (dd *directDialer) dialUDP(ctx context.Context, addr string) (net.Conn, error) {
	return dd.d.DialContext(ctx, "udp", addr)
}
