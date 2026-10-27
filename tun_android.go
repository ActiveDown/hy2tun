package main

import (
	"errors"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
	"golang.zx2c4.com/wireguard/tun"
)

// createTUN asks the app for the VPN interface (VpnService.Builder.establish():
// the VPN permission, the foreground service, the addresses, routes and the
// per-app list) and takes over its file descriptor.
func createTUN(name string, mtu int) (tun.Device, error) {
	res := establishVpn() // "fd:<n>" or "error:<text>"
	if msg, ok := strings.CutPrefix(res, "error:"); ok {
		return nil, errors.New(msg)
	}
	fd, err := strconv.Atoi(strings.TrimPrefix(res, "fd:"))
	if err != nil || fd < 0 {
		return nil, errors.New("no VPN interface from VpnService")
	}
	dev, _, err := tun.CreateUnmonitoredTUNFromFD(fd)
	if err != nil {
		unix.Close(fd)
		return nil, err
	}
	return &appTun{Device: dev}, nil
}

// appTun tells the app right before the tunnel's interface goes away, so the
// kill switch can block the selected apps first (no window in which they could
// leave over the physical network).
type appTun struct {
	tun.Device
	once sync.Once
}

func (t *appTun) Close() error {
	t.once.Do(tunClosing)
	return t.Device.Close()
}
