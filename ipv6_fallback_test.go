package main

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/apernet/hysteria/core/v2/client"
	coreErrs "github.com/apernet/hysteria/core/v2/errors"
	"github.com/apernet/hysteria/core/v2/server"
)

type fallbackTestClient struct {
	tcp func(string) (net.Conn, error)
}

func (c *fallbackTestClient) TCP(addr string) (net.Conn, error) { return c.tcp(addr) }
func (c *fallbackTestClient) UDP() (client.HyUDPConn, error) {
	return nil, errors.New("unexpected UDP call")
}
func (c *fallbackTestClient) Close() error { return nil }

func TestDialTunnelTCPIPv6Fallback(t *testing.T) {
	const v6 = "[2001:db8::1]:443"
	dialErr := coreErrs.DialError{Message: "network is unreachable"}
	fallbackErr := errors.New("fallback failed")
	for _, tc := range []struct {
		name        string
		dst         string
		host        string
		firstErr    error
		fallbackErr error
		want        []string
		wantErr     error
	}{
		{name: "working IPv6 keeps chosen IP", dst: v6, host: "example.test", want: []string{v6}},
		{name: "IPv6 retries known name", dst: v6, host: "example.test.", firstErr: dialErr, want: []string{v6, "example.test:443"}},
		{name: "wrapped dial error", dst: v6, host: "example.test", firstErr: fmt.Errorf("wrapped: %w", dialErr), want: []string{v6, "example.test:443"}},
		{name: "IPv4 unchanged", dst: "192.0.2.1:443", host: "example.test", firstErr: dialErr, want: []string{"192.0.2.1:443"}, wantErr: dialErr},
		{name: "mapped IPv4 unchanged", dst: "[::ffff:192.0.2.1]:443", host: "example.test", firstErr: dialErr, want: []string{"[::ffff:192.0.2.1]:443"}, wantErr: dialErr},
		{name: "unknown name unchanged", dst: v6, firstErr: dialErr, want: []string{v6}, wantErr: dialErr},
		{name: "IPv4 name unchanged", dst: v6, host: "192.0.2.1", firstErr: dialErr, want: []string{v6}, wantErr: dialErr},
		{name: "IPv6 name unchanged", dst: v6, host: "2001:db8::2", firstErr: dialErr, want: []string{v6}, wantErr: dialErr},
		{name: "malformed name unchanged", dst: v6, host: "example.test:8443", firstErr: dialErr, want: []string{v6}, wantErr: dialErr},
		{name: "auth error not retried", dst: v6, host: "example.test", firstErr: coreErrs.AuthError{StatusCode: 403}, want: []string{v6}, wantErr: coreErrs.AuthError{StatusCode: 403}},
		{name: "protocol error not retried", dst: v6, host: "example.test", firstErr: coreErrs.ProtocolError{Message: "bad response"}, want: []string{v6}, wantErr: coreErrs.ProtocolError{Message: "bad response"}},
		{name: "closed transport not retried", dst: v6, host: "example.test", firstErr: coreErrs.ClosedError{}, want: []string{v6}, wantErr: coreErrs.ClosedError{}},
		{name: "failed fallback returned once", dst: v6, host: "example.test", firstErr: dialErr, fallbackErr: fallbackErr, want: []string{v6, "example.test:443"}, wantErr: fallbackErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, peer := net.Pipe()
			defer conn.Close()
			defer peer.Close()
			var got []string
			hc := &fallbackTestClient{tcp: func(addr string) (net.Conn, error) {
				got = append(got, addr)
				err := tc.firstErr
				if len(got) > 1 {
					err = tc.fallbackErr
				}
				if err != nil {
					return nil, err
				}
				return conn, nil
			}}
			tn := &tunnel{hc: hc}
			gotConn, err := tn.dialTunnelTCP(tc.dst, tc.host)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("dial attempts = %v, want %v", got, tc.want)
			}
			if err == nil && gotConn != conn {
				t.Fatal("successful connection was not returned")
			}
		})
	}
}

// fallbackTestOutbound models an IPv4-only VPS without depending on the host's
// actual IPv6 routes or external DNS: the IPv6 literal fails, while the name
// resolves to an IPv4 loopback echo server.
type fallbackTestOutbound struct {
	echo string
	mu   sync.Mutex
	seen []string
}

func (o *fallbackTestOutbound) TCP(addr string) (net.Conn, error) {
	o.mu.Lock()
	o.seen = append(o.seen, addr)
	o.mu.Unlock()
	if addr != "ipv4-only.hy2tun.test:443" {
		return nil, errors.New("network is unreachable")
	}
	return net.DialTimeout("tcp4", o.echo, 5*time.Second)
}

func (o *fallbackTestOutbound) UDP(string) (server.UDPConn, error) {
	return nil, errors.New("UDP disabled in this test")
}
func (o *fallbackTestOutbound) CheckUDP(string) error { return nil }

func TestDialTunnelTCPIPv6FallbackThroughHysteria(t *testing.T) {
	cert, _ := selfSigned(t)
	pc, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	out := &fallbackTestOutbound{echo: startEcho(t)}
	srv, err := server.NewServer(&server.Config{
		TLSConfig:     server.TLSConfig{Certificates: []tls.Certificate{cert}},
		Conn:          pc,
		Authenticator: testAuth{pw: "pw"},
		Outbound:      out,
		DisableUDP:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	defer srv.Close()
	l, err := parseLink(fmt.Sprintf("hy2://pw@127.0.0.1:%d/?sni=hy2tun.test&insecure=1", pc.LocalAddr().(*net.UDPAddr).Port))
	if err != nil {
		t.Fatal(err)
	}
	hc, err := newHysteriaClient(l, net.IPv4(127, 0, 0, 1), false)
	if err != nil {
		t.Fatal(err)
	}
	defer hc.Close()
	tn := &tunnel{hc: hc}
	const dst = "[2001:db8::1]:443"
	conn, err := tn.dialTunnelTCP(dst, "ipv4-only.hy2tun.test.")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	const msg = "hello after the IPv6 dial was rejected"
	if _, err := io.WriteString(conn, msg); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != msg {
		t.Fatalf("echo = %q, want %q", got, msg)
	}
	out.mu.Lock()
	defer out.mu.Unlock()
	if want := []string{dst, "ipv4-only.hy2tun.test:443"}; !reflect.DeepEqual(out.seen, want) {
		t.Fatalf("server dial requests = %v, want %v", out.seen, want)
	}
}
