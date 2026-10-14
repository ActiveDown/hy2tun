package main

import (
	"strings"
	"testing"
	"time"
)

func TestParseLink(t *testing.T) {
	pin := strings.Repeat("ab", 32)
	pinColon := strings.ToUpper(strings.TrimSuffix(strings.Repeat("ab:", 32), ":"))

	cases := []struct {
		name string
		in   string
		want hy2Link
	}{
		{"plain", "hy2://secret@example.com:8443/?sni=cdn.example.com&insecure=1#My%20server",
			hy2Link{Host: "example.com", Port: "8443", Auth: "secret", SNI: "cdn.example.com", Insecure: true, Name: "My server"}},
		{"default port and sni", "hysteria2://pw@example.com",
			hy2Link{Host: "example.com", Port: "443", Auth: "pw", SNI: "example.com"}},
		{"user:pass auth", "hy2://user:pass@example.com:443/",
			hy2Link{Host: "example.com", Port: "443", Auth: "user:pass", SNI: "example.com"}},
		{"percent-encoded auth", "hy2://p%40ss%2Fw@example.com:443",
			hy2Link{Host: "example.com", Port: "443", Auth: "p@ss/w", SNI: "example.com"}},
		{"salamander", "hy2://pw@example.com:443/?obfs=salamander&obfs-password=hunter2",
			hy2Link{Host: "example.com", Port: "443", Auth: "pw", SNI: "example.com", Obfs: "salamander", ObfsPassword: "hunter2"}},
		{"gecko", "hy2://pw@example.com:443/?obfs=gecko&obfs-password=x",
			hy2Link{Host: "example.com", Port: "443", Auth: "pw", SNI: "example.com", Obfs: "gecko", ObfsPassword: "x"}},
		{"ports in authority", "hy2://pw@example.com:20000-30000/",
			hy2Link{Host: "example.com", Port: "20000", Ports: "20000-30000", Auth: "pw", SNI: "example.com"}},
		{"mixed port set", "hy2://pw@example.com:443,5000-6000/",
			hy2Link{Host: "example.com", Port: "443", Ports: "443,5000-6000", Auth: "pw", SNI: "example.com"}},
		{"mport", "hy2://pw@example.com:443/?mport=20000-30000",
			hy2Link{Host: "example.com", Port: "443", Ports: "20000-30000", Auth: "pw", SNI: "example.com"}},
		{"mport without own port", "hy2://pw@example.com/?mport=20000-30000",
			hy2Link{Host: "example.com", Port: "20000", Ports: "20000-30000", Auth: "pw", SNI: "example.com"}},
		{"hop interval", "hy2://pw@example.com:443/?mport=20000-30000&hop-interval=15",
			hy2Link{Host: "example.com", Port: "443", Ports: "20000-30000", Auth: "pw", SNI: "example.com", HopInterval: 15 * time.Second}},
		{"ipv6", "hy2://pw@[2001:db8::1]:8443/",
			hy2Link{Host: "2001:db8::1", Port: "8443", Auth: "pw", SNI: "2001:db8::1"}},
		{"ipv6 default port", "hy2://pw@[2001:db8::1]/",
			hy2Link{Host: "2001:db8::1", Port: "443", Auth: "pw", SNI: "2001:db8::1"}},
		{"ipv6 hopping", "hy2://pw@[2001:db8::1]:20000-30000/",
			hy2Link{Host: "2001:db8::1", Port: "20000", Ports: "20000-30000", Auth: "pw", SNI: "2001:db8::1"}},
		{"pin", "hy2://pw@example.com:443/?pinSHA256=" + pinColon,
			hy2Link{Host: "example.com", Port: "443", Auth: "pw", SNI: "example.com", PinSHA256: pin}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseLink(c.in)
			if err != nil {
				t.Fatalf("parseLink: %v", err)
			}
			if *got != c.want {
				t.Fatalf("got  %+v\nwant %+v", *got, c.want)
			}
		})
	}
}

func TestParseLinkErrors(t *testing.T) {
	bad := map[string]string{
		"scheme":             "https://example.com",
		"no host":            "hy2://pw@:443/",
		"bad port":           "hy2://pw@example.com:99999/",
		"port zero":          "hy2://pw@example.com:0/",
		"reversed range":     "hy2://pw@example.com:30000-20000/",
		"text port":          "hy2://pw@example.com:https/",
		"bad mport":          "hy2://pw@example.com/?mport=a-b",
		"obfs without pass":  "hy2://pw@example.com/?obfs=salamander",
		"pass without obfs":  "hy2://pw@example.com/?obfs-password=x",
		"unknown obfs":       "hy2://pw@example.com/?obfs=snowflake&obfs-password=x",
		"short pin":          "hy2://pw@example.com/?pinSHA256=abcd",
		"non-hex pin":        "hy2://pw@example.com/?pinSHA256=" + strings.Repeat("zz", 32),
		"hop interval small": "hy2://pw@example.com/?mport=1000-2000&hop-interval=1",
		"unclosed v6":        "hy2://pw@[2001:db8::1:443/",
	}
	for name, in := range bad {
		t.Run(name, func(t *testing.T) {
			if l, err := parseLink(in); err == nil {
				t.Fatalf("expected an error, got %+v", *l)
			}
		})
	}
}
