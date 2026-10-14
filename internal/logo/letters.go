package logo

import (
	"image"
	"image/color"
	"math"
)

// ---- a tiny stroke font: just the glyphs the monograms need ----

type pt struct{ x, y float64 }
type seg struct{ a, b pt }

// glyph strokes live in a unit box: x 0..1, y 0..1 (y grows downwards).
func glyphSegs(r rune) []seg {
	path := func(ps ...pt) []seg {
		var out []seg
		for i := 0; i+1 < len(ps); i++ {
			out = append(out, seg{ps[i], ps[i+1]})
		}
		return out
	}
	switch r {
	case 'H':
		return []seg{{pt{0, 0}, pt{0, 1}}, {pt{1, 0}, pt{1, 1}}, {pt{0, .5}, pt{1, .5}}}
	case 'T':
		return []seg{{pt{0, 0}, pt{1, 0}}, {pt{.5, 0}, pt{.5, 1}}}
	case '2':
		var ps []pt
		for deg := 190; deg <= 360+35; deg += 12 { // top bowl, clockwise
			a := float64(deg) * math.Pi / 180
			ps = append(ps, pt{.5 + .5*math.Cos(a), .29 + .29*math.Sin(a)})
		}
		ps = append(ps, pt{0, 1}, pt{1, 1})
		return path(ps...)
	case 'y':
		return []seg{{pt{0, .30}, pt{.5, .84}}, {pt{1, .30}, pt{.30, 1.06}}}
	}
	return nil
}

func distToSeg(p, a, b pt) float64 {
	dx, dy := b.x-a.x, b.y-a.y
	l2 := dx*dx + dy*dy
	t := 0.0
	if l2 > 0 {
		t = ((p.x-a.x)*dx + (p.y-a.y)*dy) / l2
		t = math.Max(0, math.Min(1, t))
	}
	return math.Hypot(p.x-(a.x+t*dx), p.y-(a.y+t*dy))
}

func segDist(a, b seg) float64 {
	// segments in these glyphs do not cross each other's interior in a way that
	// matters for spacing; the minimum of the endpoint distances is exact for
	// non-intersecting segments and 0 is returned for crossings.
	if segCross(a, b) {
		return 0
	}
	d := distToSeg(a.a, b.a, b.b)
	d = math.Min(d, distToSeg(a.b, b.a, b.b))
	d = math.Min(d, distToSeg(b.a, a.a, a.b))
	d = math.Min(d, distToSeg(b.b, a.a, a.b))
	return d
}

func segCross(p, q seg) bool {
	o := func(a, b, c pt) float64 { return (b.x-a.x)*(c.y-a.y) - (b.y-a.y)*(c.x-a.x) }
	d1, d2 := o(q.a, q.b, p.a), o(q.a, q.b, p.b)
	d3, d4 := o(p.a, p.b, q.a), o(p.a, p.b, q.b)
	return d1*d2 < 0 && d3*d4 < 0
}

func minClearance(a, b []seg) float64 {
	m := math.MaxFloat64
	for _, x := range a {
		for _, y := range b {
			if d := segDist(x, y); d < m {
				m = d
			}
		}
	}
	return m
}

func shifted(g []seg, dx, dy float64) []seg {
	out := make([]seg, len(g))
	for i, s := range g {
		out[i] = seg{pt{s.a.x + dx, s.a.y + dy}, pt{s.b.x + dx, s.b.y + dy}}
	}
	return out
}

func maxX(g []seg) float64 {
	m := -math.MaxFloat64
	for _, s := range g {
		m = math.Max(m, math.Max(s.a.x, s.b.x))
	}
	return m
}

// layout places the glyphs of text with an optical rhythm: every glyph is moved
// right until the clear space between its strokes and the previous glyph's
// strokes equals gap (stroke edges, not glyph boxes), then the whole word is
// centred in a size x size canvas by its real ink bounds. h is the cap height,
// w the glyph width.
func layout(text string, size, h, w, gap, stroke float64) []seg {
	step := math.Max(size/512, 0.25)
	var placed []seg
	var prev []seg
	for _, r := range text {
		var g []seg
		for _, s := range glyphSegs(r) {
			g = append(g, seg{pt{s.a.x * w, s.a.y * h}, pt{s.b.x * w, s.b.y * h}})
		}
		if prev == nil {
			placed = append(placed, g...)
			prev = g
			continue
		}
		x := maxX(prev) - .35*w // allow limited tucking (e.g. "y" next to "H")
		for minClearance(prev, shifted(g, x, 0)) < stroke+gap {
			x += step
		}
		sg := shifted(g, x, 0)
		placed = append(placed, sg...)
		prev = sg
	}
	// centre by ink bounds (strokes have round caps of radius stroke/2)
	minX, minY := math.MaxFloat64, math.MaxFloat64
	maxXv, maxY := -math.MaxFloat64, -math.MaxFloat64
	for _, s := range placed {
		for _, p := range []pt{s.a, s.b} {
			minX, maxXv = math.Min(minX, p.x), math.Max(maxXv, p.x)
			minY, maxY = math.Min(minY, p.y), math.Max(maxY, p.y)
		}
	}
	cx, cy := (minX+maxXv)/2, (minY+maxY)/2
	return shifted(placed, size/2-cx, size/2-cy)
}

// strokeCoverage rasterises round-capped strokes of the given width.
func strokeCoverage(size int, segs []seg, width float64) [][]float64 {
	const ss = 3
	cov := make([][]float64, size)
	hw := width / 2
	for y := 0; y < size; y++ {
		cov[y] = make([]float64, size)
		for x := 0; x < size; x++ {
			n := 0
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					p := pt{float64(x) + (float64(sx)+.5)/ss, float64(y) + (float64(sy)+.5)/ss}
					for _, s := range segs {
						if distToSeg(p, s.a, s.b) <= hw {
							n++
							break
						}
					}
				}
			}
			cov[y][x] = float64(n) / (ss * ss)
		}
	}
	return cov
}

func over(dst *image.NRGBA, x, y int, c color.NRGBA, a float64) {
	if a <= 0 {
		return
	}
	d := dst.NRGBAAt(x, y)
	da := float64(d.A) / 255
	oa := a + da*(1-a)
	if oa == 0 {
		return
	}
	mix := func(s, t uint8) uint8 {
		return uint8((float64(s)*a + float64(t)*da*(1-a)) / oa)
	}
	dst.SetNRGBA(x, y, color.NRGBA{mix(c.R, d.R), mix(c.G, d.G), mix(c.B, d.B), uint8(oa * 255)})
}

func gradAt(size, x, y int) color.NRGBA {
	t := (float64(x) + float64(y)) / (2 * float64(size-1))
	return color.NRGBA{
		R: uint8(colA[0] + (colB[0]-colA[0])*t),
		G: uint8(colA[1] + (colB[1]-colA[1])*t),
		B: uint8(colA[2] + (colB[2]-colA[2])*t),
		A: 255,
	}
}

// ---- variants ----

// Variant is one icon candidate.
type Variant struct {
	Name string
	Draw func(size int) *image.NRGBA
}

// maxInk is the widest the lettering may get, as a fraction of the tile; it
// leaves a clear margin to the rounded edges.
const maxInk = 0.72

func inkWidth(segs []seg, stroke float64) float64 {
	lo, hi := math.MaxFloat64, -math.MaxFloat64
	for _, s := range segs {
		lo = math.Min(lo, math.Min(s.a.x, s.b.x))
		hi = math.Max(hi, math.Max(s.a.x, s.b.x))
	}
	return hi - lo + stroke
}

func text(img *image.NRGBA, s string, h, w, g, stroke float64, paint func(x, y int) color.NRGBA) {
	size := img.Bounds().Dx()
	fs := float64(size)
	segs := layout(s, fs, h, w, g, stroke)
	if iw := inkWidth(segs, stroke); iw > maxInk*fs { // scale the whole word to fit
		f := maxInk * fs / iw
		h, w, g, stroke = h*f, w*f, g*f, stroke*f
		segs = layout(s, fs, h, w, g, stroke)
	}
	cov := strokeCoverage(size, segs, stroke)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if c := cov[y][x]; c > 0 {
				over(img, x, y, paint(x, y), c)
			}
		}
	}
}

func white(x, y int) color.NRGBA { return color.NRGBA{255, 255, 255, 255} }

func darkTile(size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	base := Render(size)
	for i := 0; i < len(base.Pix); i += 4 {
		if base.Pix[i+3] > 0 {
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 20, 22, 30, base.Pix[i+3]
		}
	}
	return img
}

func circleBadge(size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	s := float64(size)
	const ss = 4
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			n := 0
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					px, py := float64(x)+(float64(sx)+.5)/ss, float64(y)+(float64(sy)+.5)/ss
					if math.Hypot(px-s/2, py-s/2) <= s*0.46 {
						n++
					}
				}
			}
			if n > 0 {
				c := gradAt(size, x, y)
				c.A = uint8(255 * n / (ss * ss))
				img.SetNRGBA(x, y, c)
			}
		}
	}
	return img
}

// Variants returns the icon candidates shown to the user.
func Variants() []Variant {
	return []Variant{
		{"1 · H2T, white on the gradient tile", func(n int) *image.NRGBA {
			img := Render(n)
			s := float64(n)
			text(img, "H2T", s*.42, s*.17, s*.06, s*.07, white)
			return img
		}},
		{"2 · Hy2T, four letters", func(n int) *image.NRGBA {
			img := Render(n)
			s := float64(n)
			text(img, "Hy2T", s*.40, s*.135, s*.05, s*.064, white)
			return img
		}},
		{"3 · H2T, gradient letters on a dark tile", func(n int) *image.NRGBA {
			img := darkTile(n)
			s := float64(n)
			text(img, "H2T", s*.42, s*.17, s*.06, s*.08, func(x, y int) color.NRGBA { return gradAt(n, x, y) })
			return img
		}},
		{"4 · H2 in a round badge", func(n int) *image.NRGBA {
			img := circleBadge(n)
			s := float64(n)
			text(img, "H2", s*.40, s*.20, s*.07, s*.085, white)
			return img
		}},
		{"5 · H2T under a tunnel arch", func(n int) *image.NRGBA {
			img := Render(n)
			s := float64(n)
			letters := layout("H2T", s, s*.22, s*.12, s*.045, s*.055)
			for i := range letters {
				letters[i].a.y += s * .20
				letters[i].b.y += s * .20
			}
			arch := []seg{}
			var prev pt
			for deg := 180; deg <= 360; deg += 6 {
				a := float64(deg) * math.Pi / 180
				p := pt{s*.5 + s*.26*math.Cos(a), s*.40 + s*.24*math.Sin(a)}
				if deg > 180 {
					arch = append(arch, seg{prev, p})
				}
				prev = p
			}
			arch = append(arch, seg{pt{s * .24, s * .40}, pt{s * .24, s * .50}}, seg{pt{s * .76, s * .40}, pt{s * .76, s * .50}})
			for _, set := range []struct {
				segs  []seg
				width float64
			}{{letters, s * .055}, {arch, s * .06}} {
				cov := strokeCoverage(n, set.segs, set.width)
				for y := 0; y < n; y++ {
					for x := 0; x < n; x++ {
						over(img, x, y, color.NRGBA{255, 255, 255, 255}, cov[y][x])
					}
				}
			}
			return img
		}},
		{"6 · H2T, outlined letters", func(n int) *image.NRGBA {
			img := Render(n)
			s := float64(n)
			segs := layout("H2T", s, s*.42, s*.17, s*.05, s*.105)
			outer := strokeCoverage(n, segs, s*.105)
			inner := strokeCoverage(n, segs, s*.042)
			for y := 0; y < n; y++ {
				for x := 0; x < n; x++ {
					over(img, x, y, color.NRGBA{255, 255, 255, 255}, outer[y][x])
				}
			}
			for y := 0; y < n; y++ {
				for x := 0; x < n; x++ {
					if inner[y][x] > 0 {
						c := gradAt(n, x, y)
						img.SetNRGBA(x, y, color.NRGBA{c.R, c.G, c.B, 255})
					}
				}
			}
			return img
		}},
	}
}

// Sheet lays the variants out on a dark background (3 columns).
func Sheet(vs []Variant, tile int) *image.NRGBA {
	cols := 3
	rows := (len(vs) + cols - 1) / cols
	pad := tile / 6
	w := cols*tile + (cols+1)*pad
	h := rows*tile + (rows+1)*pad
	sheet := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := range sheet.Pix {
		if i%4 == 3 {
			sheet.Pix[i] = 255
		} else {
			sheet.Pix[i] = 14
		}
	}
	for i, v := range vs {
		im := v.Draw(tile)
		ox := pad + (i%cols)*(tile+pad)
		oy := pad + (i/cols)*(tile+pad)
		for y := 0; y < tile; y++ {
			for x := 0; x < tile; x++ {
				c := im.NRGBAAt(x, y)
				if c.A > 0 {
					over(sheet, ox+x, oy+y, c, float64(c.A)/255)
				}
			}
		}
	}
	return sheet
}

// App is the application icon: the "Hy2T" monogram on the gradient tile. At
// small sizes the letters are simplified so that the mark stays readable
// ("H2T" up to 47 px, "H2" below 24 px).
func App(size int) *image.NRGBA {
	img := Render(size)
	s := float64(size)
	switch {
	case size >= 48:
		text(img, "Hy2T", s*.40, s*.135, s*.048, s*.066, white)
	case size >= 24:
		text(img, "H2T", s*.42, s*.17, s*.06, s*.095, white)
	default:
		text(img, "H2", s*.46, s*.24, s*.07, s*.14, white)
	}
	return img
}
