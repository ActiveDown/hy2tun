package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"fmt"
	"io"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/apernet/hysteria/core/v2/server"
	"github.com/apernet/hysteria/extras/v2/obfs"
)

type testAuth struct{ pw string }

func (a testAuth) Authenticate(_ net.Addr, auth string, _ uint64) (bool, string) {
	return auth == a.pw, "test"
}

func selfSigned(t *testing.T) (tls.Certificate, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "hy2tun.test"},
		DNSNames:     []string{"hy2tun.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, hex.EncodeToString(sum[:])
}

// startServer runs a real Hysteria2 server (optionally behind Salamander) on
// 127.0.0.1 and returns its UDP port.
func startServer(t *testing.T, cert tls.Certificate, pw, salamanderPW string) int {
	t.Helper()
	var pc net.PacketConn
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	if salamanderPW != "" {
		if pc, err = obfs.WrapPacketConnSalamander(pc, []byte(salamanderPW)); err != nil {
			t.Fatal(err)
		}
	}
	srv, err := server.NewServer(&server.Config{
		TLSConfig:     server.TLSConfig{Certificates: []tls.Certificate{cert}},
		Conn:          pc,
		Authenticator: testAuth{pw},
	})
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	t.Cleanup(func() { srv.Close() })
	return pc.LocalAddr().(*net.UDPAddr).Port
}

func startEcho(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	return ln.Addr().String()
}

// roundTrip connects with the link and bounces a message off the echo server.
func roundTrip(t *testing.T, link, echo string) error {
	t.Helper()
	l, err := parseLink(link)
	if err != nil {
		t.Fatalf("parseLink(%q): %v", link, err)
	}
	hc, err := newHysteriaClient(l, net.IPv4(127, 0, 0, 1), false)
	if err != nil {
		return err
	}
	defer hc.Close()
	conn, err := hc.TCP(echo)
	if err != nil {
		return err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	msg := "hello through hysteria2"
	if _, err := io.WriteString(conn, msg); err != nil {
		return err
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		return err
	}
	if string(buf) != msg {
		return fmt.Errorf("echo mismatch: %q", buf)
	}
	return nil
}

func TestTransportPlain(t *testing.T) {
	cert, _ := selfSigned(t)
	port := startServer(t, cert, "pw", "")
	echo := startEcho(t)
	link := fmt.Sprintf("hy2://pw@127.0.0.1:%d/?sni=hy2tun.test&insecure=1", port)
	if err := roundTrip(t, link, echo); err != nil {
		t.Fatal(err)
	}
}

func TestTransportSalamander(t *testing.T) {
	cert, _ := selfSigned(t)
	port := startServer(t, cert, "pw", "obfs-secret")
	echo := startEcho(t)

	link := fmt.Sprintf("hy2://pw@127.0.0.1:%d/?sni=hy2tun.test&insecure=1&obfs=salamander&obfs-password=obfs-secret", port)
	if err := roundTrip(t, link, echo); err != nil {
		t.Fatalf("with the right obfs password: %v", err)
	}

	// a wrong password, or none at all, must not get through
	for name, q := range map[string]string{
		"wrong password": "&obfs=salamander&obfs-password=nope",
		"no obfs":        "",
	} {
		t.Run(name, func(t *testing.T) {
			bad := fmt.Sprintf("hy2://pw@127.0.0.1:%d/?sni=hy2tun.test&insecure=1%s", port, q)
			done := make(chan error, 1)
			go func() { done <- roundTrip(t, bad, echo) }()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("the connection succeeded without the right obfs password")
				}
			case <-time.After(20 * time.Second):
				// still handshaking forever is also "did not get through"
			}
		})
	}
}

func TestTransportPortHopping(t *testing.T) {
	cert, _ := selfSigned(t)
	port := startServer(t, cert, "pw", "")
	echo := startEcho(t)
	// a one-port range still goes through the hopping socket
	link := fmt.Sprintf("hy2://pw@127.0.0.1:%d-%d/?sni=hy2tun.test&insecure=1&hop-interval=5", port, port)
	l, _ := parseLink(link)
	if !l.Hops() {
		t.Fatalf("link %q should hop", link)
	}
	if err := roundTrip(t, link, echo); err != nil {
		t.Fatal(err)
	}
}

func TestTransportHoppingWithSalamander(t *testing.T) {
	cert, _ := selfSigned(t)
	port := startServer(t, cert, "pw", "s3cret")
	echo := startEcho(t)
	link := fmt.Sprintf("hy2://pw@127.0.0.1/?mport=%d-%d&sni=hy2tun.test&insecure=1&obfs=salamander&obfs-password=s3cret", port, port)
	if err := roundTrip(t, link, echo); err != nil {
		t.Fatal(err)
	}
}

func TestTransportPinSHA256(t *testing.T) {
	cert, pin := selfSigned(t)
	port := startServer(t, cert, "pw", "")
	echo := startEcho(t)

	// a pin works on its own: the certificate is self-signed and not "insecure"
	good := fmt.Sprintf("hy2://pw@127.0.0.1:%d/?sni=hy2tun.test&pinSHA256=%s", port, pin)
	if err := roundTrip(t, good, echo); err != nil {
		t.Fatalf("matching pin: %v", err)
	}

	wrong := strings.Repeat("00", 32)
	bad := fmt.Sprintf("hy2://pw@127.0.0.1:%d/?sni=hy2tun.test&pinSHA256=%s", port, wrong)
	if err := roundTrip(t, bad, echo); err == nil {
		t.Fatal("a wrong pin was accepted")
	}
}

func TestTransportHopAddressing(t *testing.T) {
	l, err := parseLink("hy2://pw@example.com:443,20000-20009/?obfs=salamander&obfs-password=x")
	if err != nil {
		t.Fatal(err)
	}
	f, addr, err := l.transport(net.IPv4(192, 0, 2, 1))
	if err != nil {
		t.Fatal(err)
	}
	if f == nil {
		t.Fatal("expected a connection factory for a hopping + obfuscated link")
	}
	if got := addr.String(); !strings.Contains(got, "192.0.2.1") {
		t.Fatalf("hop address %q does not carry the server IP", got)
	}

	// no obfs and no hopping keeps the library's default socket
	l, _ = parseLink("hy2://pw@example.com:443/")
	if f, _, err = l.transport(net.IPv4(192, 0, 2, 1)); err != nil || f != nil {
		t.Fatalf("plain link: factory=%v err=%v", f, err)
	}
}
