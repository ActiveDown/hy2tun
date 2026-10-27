package main

import "testing"

func TestDNSScore(t *testing.T) {
	hdr := func(rcode, an byte) []byte {
		b := make([]byte, 12)
		b[3] = 0x80 | rcode
		b[7] = an
		return b
	}
	for _, c := range []struct {
		resp []byte
		want int
	}{
		{hdr(0, 1), 2},
		{hdr(0, 0), 1},
		{hdr(3, 0), 0},
		{hdr(2, 0), 0},
		{[]byte{1, 2}, 0},
	} {
		if got := dnsScore(c.resp); got != c.want {
			t.Errorf("dnsScore(% x) = %d, want %d", c.resp, got, c.want)
		}
	}
}
