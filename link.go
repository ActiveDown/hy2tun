package main

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// hy2Link is a parsed hy2:// / hysteria2:// share link.
type hy2Link struct {
	Host     string
	Port     string
	Auth     string
	SNI      string
	Insecure bool
	Obfs     string
}

func parseLink(raw string) (*hy2Link, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	if u.Scheme != "hy2" && u.Scheme != "hysteria2" {
		return nil, fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	auth := u.User.Username()
	if p, ok := u.User.Password(); ok {
		auth += ":" + p
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	q := u.Query()
	l := &hy2Link{
		Host:     u.Hostname(),
		Port:     port,
		Auth:     auth,
		SNI:      q.Get("sni"),
		Insecure: q.Get("insecure") == "1",
		Obfs:     q.Get("obfs"),
	}
	if l.SNI == "" {
		l.SNI = l.Host
	}
	return l, nil
}

func (l *hy2Link) hostPort() string { return net.JoinHostPort(l.Host, l.Port) }
