//go:build darwin

package main

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// tunOffset is the headroom wireguard-go's utun device expects in front of every
// packet passed to Read/Write (the 4-byte address-family header). Offset 0 makes
// Read slice out of range and panic, which kills the whole service.
const tunOffset = 4

// directDialer opens sockets that are bound to the physical interface, so they
// bypass the TUN default routes. The interface can change at run time (Wi-Fi <->
// cable); setIfIndex updates it for all future sockets.
type directDialer struct {
	ifIndex atomic.Uint32
	d       net.Dialer
}

func parseIfIndex(s string) (uint32, error) {
	n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 32)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("bad interface index %q", s)
	}
	return uint32(n), nil
}

func newDirectDialer(ifIndex string) (*directDialer, error) {
	n, err := parseIfIndex(ifIndex)
	if err != nil {
		return nil, err
	}
	dd := &directDialer{}
	dd.ifIndex.Store(n)
	dd.d = net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(network, address string, c syscall.RawConn) error {
			idx := dd.ifIndex.Load()
			var serr error
			err := c.Control(func(fd uintptr) {
				if strings.HasSuffix(network, "6") {
					serr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IPV6, unix.IPV6_BOUND_IF, int(idx))
				} else {
					serr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_BOUND_IF, int(idx))
				}
			})
			if err != nil {
				return err
			}
			return serr
		},
	}
	return dd, nil
}

func (dd *directDialer) setIfIndex(idx uint32) { dd.ifIndex.Store(idx) }

func (dd *directDialer) dialTCP(ctx context.Context, addr string) (net.Conn, error) {
	return dd.d.DialContext(ctx, "tcp", addr)
}

func (dd *directDialer) dialUDP(ctx context.Context, addr string) (net.Conn, error) {
	return dd.d.DialContext(ctx, "udp", addr)
}
