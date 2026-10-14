// Package logo draws the Hy2Tun application icon: a rounded square with a
// diagonal violet-to-cyan gradient (the same mark that is used in the UI).
package logo

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"math"
)

var (
	colA = [3]float64{0x7a, 0x5c, 0xff} // top-left
	colB = [3]float64{0x00, 0xc2, 0xff} // bottom-right
)

// Render draws the logo at size x size pixels with anti-aliased corners.
func Render(size int) *image.NRGBA {
	const ss = 4 // supersampling
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	s := float64(size)
	radius := s * 0.24
	margin := s * 0.04
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var cov float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					px := float64(x) + (float64(sx)+0.5)/ss
					py := float64(y) + (float64(sy)+0.5)/ss
					if inRoundRect(px, py, margin, margin, s-margin, s-margin, radius) {
						cov++
					}
				}
			}
			if cov == 0 {
				continue
			}
			t := (float64(x) + float64(y)) / (2 * (s - 1)) // 135deg gradient
			c := color.NRGBA{
				R: uint8(colA[0] + (colB[0]-colA[0])*t),
				G: uint8(colA[1] + (colB[1]-colA[1])*t),
				B: uint8(colA[2] + (colB[2]-colA[2])*t),
				A: uint8(255 * cov / (ss * ss)),
			}
			img.SetNRGBA(x, y, c)
		}
	}
	return img
}

func inRoundRect(px, py, x0, y0, x1, y1, r float64) bool {
	if px < x0 || px > x1 || py < y0 || py > y1 {
		return false
	}
	cx := math.Min(math.Max(px, x0+r), x1-r)
	cy := math.Min(math.Max(py, y0+r), y1-r)
	dx, dy := px-cx, py-cy
	return dx*dx+dy*dy <= r*r
}

// Gray desaturates the image (used for the "disconnected" tray state).
func Gray(src *image.NRGBA) *image.NRGBA {
	out := image.NewNRGBA(src.Bounds())
	for i := 0; i < len(src.Pix); i += 4 {
		r, g, b, a := float64(src.Pix[i]), float64(src.Pix[i+1]), float64(src.Pix[i+2]), src.Pix[i+3]
		l := uint8(0.3*r + 0.59*g + 0.11*b)
		// keep it light enough to be visible on dark and light trays
		l = uint8(float64(l)*0.55 + 70)
		out.Pix[i], out.Pix[i+1], out.Pix[i+2], out.Pix[i+3] = l, l, l, a
	}
	return out
}

// Dot paints a round status badge in the bottom-right corner.
func Dot(src *image.NRGBA, c color.NRGBA) *image.NRGBA {
	out := image.NewNRGBA(src.Bounds())
	copy(out.Pix, src.Pix)
	s := float64(src.Bounds().Dx())
	r := s * 0.26
	cx, cy := s-r-s*0.01, s-r-s*0.01
	ring := color.NRGBA{16, 18, 24, 255}
	for y := 0; y < src.Bounds().Dy(); y++ {
		for x := 0; x < src.Bounds().Dx(); x++ {
			d := math.Hypot(float64(x)+0.5-cx, float64(y)+0.5-cy)
			switch {
			case d <= r-s*0.045:
				out.SetNRGBA(x, y, c)
			case d <= r:
				out.SetNRGBA(x, y, ring)
			}
		}
	}
	return out
}

// PNG encodes img.
func PNG(img image.Image) []byte {
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

// ICO packs the images (PNG-compressed entries, Windows Vista and later).
func ICO(imgs ...*image.NRGBA) []byte {
	type entry struct {
		w, h int
		data []byte
	}
	var es []entry
	for _, im := range imgs {
		es = append(es, entry{im.Bounds().Dx(), im.Bounds().Dy(), PNG(im)})
	}
	var out bytes.Buffer
	binary.Write(&out, binary.LittleEndian, []uint16{0, 1, uint16(len(es))})
	offset := 6 + 16*len(es)
	for _, e := range es {
		w, h := byte(e.w), byte(e.h)
		if e.w >= 256 {
			w = 0
		}
		if e.h >= 256 {
			h = 0
		}
		out.Write([]byte{w, h, 0, 0})
		binary.Write(&out, binary.LittleEndian, []uint16{1, 32})
		binary.Write(&out, binary.LittleEndian, uint32(len(e.data)))
		binary.Write(&out, binary.LittleEndian, uint32(offset))
		offset += len(e.data)
	}
	for _, e := range es {
		out.Write(e.data)
	}
	return out.Bytes()
}
