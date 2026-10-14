package main

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"strconv"
	"strings"

	"github.com/apernet/hysteria/core/v2/client"
	"github.com/apernet/hysteria/extras/v2/obfs"
	"github.com/apernet/hysteria/extras/v2/transport/udphop"
)

// connFactory builds the UDP socket Hysteria2 runs on: a plain socket, or a
// port-hopping one, optionally wrapped in Salamander / Gecko obfuscation (the
// obfuscation is the outer layer, exactly like in the official client).
type connFactory struct {
	l   *hy2Link
	hop *udphop.UDPHopAddr // nil = a single port
}

func (f *connFactory) New(net.Addr) (net.PacketConn, error) {
	var pc net.PacketConn
	var err error
	if f.hop != nil {
		cfg := udphop.HopIntervalConfig{}
		if f.l.HopInterval > 0 {
			cfg = udphop.HopIntervalConfig{Min: f.l.HopInterval, Max: f.l.HopInterval}
		}
		pc, err = udphop.NewUDPHopPacketConn(f.hop, cfg, func() (net.PacketConn, error) {
			return net.ListenUDP("udp", nil)
		})
	} else {
		pc, err = net.ListenUDP("udp", nil)
	}
	if err != nil {
		return nil, err
	}
	switch f.l.Obfs {
	case "":
		return pc, nil
	case "salamander":
		w, err := obfs.WrapPacketConnSalamander(pc, []byte(f.l.ObfsPassword))
		if err != nil {
			pc.Close()
			return nil, err
		}
		return w, nil
	case "gecko":
		w, err := obfs.WrapPacketConnGecko(pc, obfs.GeckoOptions{Password: []byte(f.l.ObfsPassword)})
		if err != nil {
			pc.Close()
			return nil, err
		}
		return w, nil
	}
	pc.Close()
	return nil, fmt.Errorf("unsupported obfs type %q", f.l.Obfs)
}

// transport returns the connection factory (nil = the library default) and the
// server address Hysteria2 should dial. ip is the already resolved and pinned
// address of the server, so hopping never depends on DNS.
func (l *hy2Link) transport(ip net.IP) (client.ConnFactory, net.Addr, error) {
	port, err := strconv.Atoi(l.Port)
	if err != nil {
		return nil, nil, fmt.Errorf("bad port %q", l.Port)
	}
	if !l.Hops() {
		addr := &net.UDPAddr{IP: ip, Port: port}
		if l.Obfs == "" {
			return nil, addr, nil
		}
		return &connFactory{l: l}, addr, nil
	}
	hop, err := udphop.ResolveUDPHopAddr(net.JoinHostPort(ip.String(), l.Ports))
	if err != nil {
		return nil, nil, fmt.Errorf("port hopping %q: %v", l.Ports, err)
	}
	return &connFactory{l: l, hop: hop}, hop, nil
}

// tlsConfig maps the link to the TLS options. A pinned certificate replaces the
// CA check (the same behaviour as the official client).
func (l *hy2Link) tlsConfig() client.TLSConfig {
	c := client.TLSConfig{ServerName: l.SNI, InsecureSkipVerify: l.Insecure}
	if l.PinSHA256 != "" {
		pin := l.PinSHA256
		c.InsecureSkipVerify = true
		c.VerifyPeerCertificate = func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return errors.New("the server sent no certificate")
			}
			sum := sha256.Sum256(rawCerts[0])
			if !strings.EqualFold(hex.EncodeToString(sum[:]), pin) {
				return errors.New("the server certificate does not match pinSHA256")
			}
			return nil
		}
	}
	return c
}

// newHysteriaClient creates the (reconnecting) Hysteria2 client for a link.
func newHysteriaClient(l *hy2Link, ip net.IP, lazy bool) (client.Client, error) {
	factory, addr, err := l.transport(ip)
	if err != nil {
		return nil, err
	}
	return client.NewReconnectableClient(func() (*client.Config, error) {
		return &client.Config{
			ServerAddr:  addr,
			Auth:        l.Auth,
			TLSConfig:   l.tlsConfig(),
			ConnFactory: factory,
		}, nil
	}, func(_ client.Client, info *client.HandshakeInfo, n int) {
		log.Printf("hysteria2 connected (attempt %d, udp relay: %v)", n, info.UDPEnabled)
	}, lazy)
}
