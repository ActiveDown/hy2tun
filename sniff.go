package main

import (
	"bytes"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"strings"
	"time"
)

var errStopHandshake = errors.New("stop")

// replayConn feeds recorded bytes to crypto/tls and swallows everything it writes.
type replayConn struct{ r io.Reader }

func (c *replayConn) Read(p []byte) (int, error)       { return c.r.Read(p) }
func (c *replayConn) Write(p []byte) (int, error)      { return len(p), nil }
func (c *replayConn) Close() error                     { return nil }
func (c *replayConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (c *replayConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (c *replayConn) SetDeadline(time.Time) error      { return nil }
func (c *replayConn) SetReadDeadline(time.Time) error  { return nil }
func (c *replayConn) SetWriteDeadline(time.Time) error { return nil }

func sniFromClientHello(data []byte) string {
	var sni string
	tc := tls.Server(&replayConn{r: bytes.NewReader(data)}, &tls.Config{
		GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
			sni = h.ServerName
			return nil, errStopHandshake
		},
	})
	tc.Handshake()
	return sni
}

func hostFromHTTP(data []byte) string {
	s := string(data)
	for _, line := range strings.Split(s, "\r\n") {
		if len(line) > 6 && strings.EqualFold(line[:5], "host:") {
			h := strings.TrimSpace(line[5:])
			if i := strings.LastIndexByte(h, ':'); i > 0 && !strings.Contains(h[i:], "]") {
				h = h[:i]
			}
			return h
		}
	}
	return ""
}

func isHTTPMethod(b []byte) bool {
	for _, m := range []string{"GET ", "POST ", "HEAD ", "PUT ", "DELETE ", "OPTIONS ", "CONNECT ", "PATCH "} {
		if bytes.HasPrefix(b, []byte(m)) {
			return true
		}
	}
	return false
}

// peekName reads the first bytes the application sends and extracts a server
// name from a TLS ClientHello or an HTTP Host header. The bytes are returned
// so the caller can forward them unchanged.
func peekName(c net.Conn, timeout time.Duration) (string, []byte) {
	deadline := time.Now().Add(timeout)
	defer c.SetReadDeadline(time.Time{})
	var data []byte
	buf := make([]byte, 4096)
	for len(data) < 16384 {
		c.SetReadDeadline(deadline)
		n, err := c.Read(buf)
		data = append(data, buf[:n]...)
		if len(data) >= 5 && data[0] == 0x16 { // TLS handshake record
			need := 5 + int(data[3])<<8 + int(data[4])
			if len(data) >= need || err != nil {
				return sniFromClientHello(data), data
			}
		} else if len(data) >= 4 {
			if isHTTPMethod(data) {
				if i := bytes.Index(data, []byte("\r\n\r\n")); i >= 0 || err != nil {
					return hostFromHTTP(data), data
				}
			} else {
				return "", data // not TLS/HTTP
			}
		}
		if err != nil {
			break
		}
	}
	return "", data
}
