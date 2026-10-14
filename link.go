package main

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// hy2Link is a parsed hy2:// / hysteria2:// share link.
//
//	hy2://[auth@]host[:port-spec]/?sni=…&insecure=1&pinSHA256=…
//	      &obfs=salamander&obfs-password=…&mport=20000-30000#Name
//
// The port spec may be a single port or a hopping set such as
// "20000-30000" or "443,5000-6000"; "mport" is the same thing as a query
// parameter (as written by sing-box, v2rayN and others).
type hy2Link struct {
	Host         string
	Port         string // the first port of the spec: shown in the UI, used when not hopping
	Ports        string // hopping set; empty when a single port is used
	Auth         string
	SNI          string
	Insecure     bool
	PinSHA256    string // lower-case hex without separators; "" = not pinned
	Obfs         string // "", "salamander" or "gecko"
	ObfsPassword string
	HopInterval  time.Duration // 0 = the library default
	Name         string        // the #fragment
}

func (l *hy2Link) hostPort() string { return net.JoinHostPort(l.Host, l.Port) }

// Hops reports whether the client has to hop between several ports.
func (l *hy2Link) Hops() bool { return l.Ports != "" }

// parsePortSpec validates "443", "20000-30000", "443,5000-6000" and returns
// the first port and whether the set contains more than one port.
func parsePortSpec(spec string) (first int, multi bool, err error) {
	if spec == "" {
		return 0, false, errors.New("empty port")
	}
	parts := strings.Split(spec, ",")
	for i, p := range parts {
		p = strings.TrimSpace(p)
		lo, hi := p, p
		if j := strings.IndexByte(p, '-'); j >= 0 {
			lo, hi = p[:j], p[j+1:]
			multi = true
		}
		a, e1 := strconv.Atoi(lo)
		b, e2 := strconv.Atoi(hi)
		if e1 != nil || e2 != nil || a < 1 || b > 65535 || a > b {
			return 0, false, fmt.Errorf("bad port %q", p)
		}
		if i == 0 {
			first = a
		}
	}
	if len(parts) > 1 {
		multi = true
	}
	return first, multi, nil
}

// splitHostPorts splits "host", "host:ports", "[v6]" and "[v6]:ports".
func splitHostPorts(hp string) (host, ports string, err error) {
	if strings.HasPrefix(hp, "[") {
		i := strings.IndexByte(hp, ']')
		if i < 0 {
			return "", "", errors.New("missing ] in the address")
		}
		host, rest := hp[1:i], hp[i+1:]
		switch {
		case rest == "":
			return host, "", nil
		case rest[0] == ':':
			return host, rest[1:], nil
		}
		return "", "", errors.New("junk after the IPv6 address")
	}
	if i := strings.LastIndexByte(hp, ':'); i >= 0 {
		return hp[:i], hp[i+1:], nil
	}
	return hp, "", nil
}

func normalizePin(s string) (string, error) {
	s = strings.ToLower(strings.NewReplacer(":", "", " ", "", "-", "").Replace(s))
	if len(s) != 64 {
		return "", errors.New("pinSHA256 must be a SHA-256 fingerprint (64 hex digits)")
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return "", errors.New("pinSHA256 must be hexadecimal")
		}
	}
	return s, nil
}

func parseLink(raw string) (*hy2Link, error) {
	raw = strings.TrimSpace(raw)
	rest := ""
	switch {
	case strings.HasPrefix(strings.ToLower(raw), "hy2://"):
		rest = raw[len("hy2://"):]
	case strings.HasPrefix(strings.ToLower(raw), "hysteria2://"):
		rest = raw[len("hysteria2://"):]
	default:
		scheme := raw
		if i := strings.Index(raw, "://"); i >= 0 {
			scheme = raw[:i]
		}
		return nil, fmt.Errorf("unsupported scheme %q", scheme)
	}

	authority, tail := rest, ""
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		authority, tail = rest[:i], rest[i:]
	}
	userinfo, hostport := "", authority
	if i := strings.LastIndexByte(authority, '@'); i >= 0 {
		userinfo, hostport = authority[:i], authority[i+1:]
	}
	auth := userinfo
	if dec, err := url.PathUnescape(userinfo); err == nil {
		auth = dec
	}

	host, portSpec, err := splitHostPorts(hostport)
	if err != nil {
		return nil, err
	}
	if host == "" {
		return nil, errors.New("the link has no host")
	}

	var q url.Values
	fragment := ""
	if u, err := url.Parse("x://h" + tail); err == nil {
		q, fragment = u.Query(), u.Fragment
	} else {
		return nil, fmt.Errorf("bad link parameters: %v", err)
	}

	l := &hy2Link{Host: host, Auth: auth, Name: fragment}

	// ports: the authority part wins, "mport" fills the gap or adds hopping
	if portSpec == "" {
		portSpec = "443"
	}
	first, multi, err := parsePortSpec(portSpec)
	if err != nil {
		return nil, err
	}
	l.Port = strconv.Itoa(first)
	if multi {
		l.Ports = portSpec
	}
	if m := strings.TrimSpace(q.Get("mport")); m != "" {
		mf, mmulti, err := parsePortSpec(m)
		if err != nil {
			return nil, fmt.Errorf("mport: %v", err)
		}
		if mmulti {
			l.Ports = m
		}
		if !strings.Contains(hostport, ":") || strings.HasSuffix(hostport, "]") {
			l.Port = strconv.Itoa(mf) // the link named no port of its own
		}
	}

	l.SNI = q.Get("sni")
	if l.SNI == "" {
		l.SNI = l.Host
	}
	switch strings.ToLower(q.Get("insecure")) {
	case "1", "true", "yes":
		l.Insecure = true
	}
	if p := q.Get("pinSHA256"); p != "" {
		if l.PinSHA256, err = normalizePin(p); err != nil {
			return nil, err
		}
	}

	l.Obfs = strings.ToLower(q.Get("obfs"))
	l.ObfsPassword = q.Get("obfs-password")
	switch l.Obfs {
	case "":
		if l.ObfsPassword != "" {
			return nil, errors.New("obfs-password is set but obfs is not")
		}
	case "salamander", "gecko":
		if l.ObfsPassword == "" {
			return nil, fmt.Errorf("obfs=%s needs obfs-password", l.Obfs)
		}
	default:
		return nil, fmt.Errorf("unsupported obfs type %q (supported: salamander, gecko)", l.Obfs)
	}

	for _, key := range []string{"hop-interval", "hopInterval"} {
		if v := q.Get(key); v != "" {
			sec, err := strconv.Atoi(v)
			if err != nil || sec < 5 {
				return nil, fmt.Errorf("%s must be a number of seconds, at least 5", key)
			}
			l.HopInterval = time.Duration(sec) * time.Second
		}
	}
	return l, nil
}
