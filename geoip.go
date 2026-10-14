package main

import (
	"bufio"
	_ "embed"
	"net/netip"
	"sort"
	"strings"
)

// ru_cidr.txt is generated from the RIPE NCC delegated statistics by
// tools/genru. Refresh it occasionally (a few new prefixes appear every week).
//
//go:embed ru_cidr.txt
var ruCIDRData string

type v4Range struct{ lo, hi uint32 }
type v6Range struct{ lo, hi [16]byte }

// geoRU answers "is this IP allocated to Russia?" with a binary search.
type geoRU struct {
	v4 []v4Range
	v6 []v6Range
}

func v4num(a [4]byte) uint32 {
	return uint32(a[0])<<24 | uint32(a[1])<<16 | uint32(a[2])<<8 | uint32(a[3])
}

func parseGeoRU(data string) *geoRU {
	g := &geoRU{}
	sc := bufio.NewScanner(strings.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		p, err := netip.ParsePrefix(line)
		if err != nil {
			continue
		}
		p = p.Masked()
		if p.Addr().Is4() {
			lo := v4num(p.Addr().As4())
			size := uint64(1) << (32 - p.Bits())
			g.v4 = append(g.v4, v4Range{lo: lo, hi: uint32(uint64(lo) + size - 1)})
		} else {
			lo := p.Addr().As16()
			hi := lo
			for bit := p.Bits(); bit < 128; bit++ {
				hi[bit/8] |= 1 << (7 - uint(bit%8))
			}
			g.v6 = append(g.v6, v6Range{lo: lo, hi: hi})
		}
	}
	sort.Slice(g.v4, func(i, j int) bool { return g.v4[i].lo < g.v4[j].lo })
	sort.Slice(g.v6, func(i, j int) bool { return string(g.v6[i].lo[:]) < string(g.v6[j].lo[:]) })
	return g
}

func (g *geoRU) contains(ip netip.Addr) bool {
	if g == nil {
		return false
	}
	ip = ip.Unmap()
	if ip.Is4() {
		n := v4num(ip.As4())
		i := sort.Search(len(g.v4), func(i int) bool { return g.v4[i].lo > n })
		return i > 0 && n <= g.v4[i-1].hi
	}
	if !ip.Is6() {
		return false
	}
	a := ip.As16()
	i := sort.Search(len(g.v6), func(i int) bool { return string(g.v6[i].lo[:]) > string(a[:]) })
	return i > 0 && string(a[:]) <= string(g.v6[i-1].hi[:])
}

func (g *geoRU) size() int { return len(g.v4) + len(g.v6) }
