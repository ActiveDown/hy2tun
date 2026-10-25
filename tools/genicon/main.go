// genicon writes assets/app.ico (multi-size) and assets/logo.png from the
// drawing in internal/logo.
//
//	go run ./tools/genicon
package main

import (
	"fmt"
	"image"
	"image/color"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/ActiveDown/hy2tun/internal/logo"
)

func main() {
	os.MkdirAll("assets", 0o755)
	if len(os.Args) > 1 && os.Args[1] == "sizes" {
		// small-size preview, enlarged with nearest-neighbour so the pixels are visible
		list := []int{16, 20, 24, 32, 48, 64}
		const zoom = 5
		w := 0
		for _, s := range list {
			w += s*zoom + 16
		}
		sheet := image.NewNRGBA(image.Rect(0, 0, w+16, 64*zoom+32))
		for i := 0; i < len(sheet.Pix); i += 4 {
			sheet.Pix[i], sheet.Pix[i+1], sheet.Pix[i+2], sheet.Pix[i+3] = 24, 26, 34, 255
		}
		x0 := 16
		for _, s := range list {
			im := logo.App(s)
			for y := 0; y < s*zoom; y++ {
				for x := 0; x < s*zoom; x++ {
					c := im.NRGBAAt(x/zoom, y/zoom)
					if c.A > 0 {
						a := float64(c.A) / 255
						sheet.SetNRGBA(x0+x, 16+y, color.NRGBA{
							uint8(float64(c.R)*a + 24*(1-a)), uint8(float64(c.G)*a + 26*(1-a)), uint8(float64(c.B)*a + 34*(1-a)), 255})
					}
				}
			}
			x0 += s*zoom + 16
		}
		os.WriteFile("assets/preview-sizes.png", logo.PNG(sheet), 0o644)
		fmt.Println("assets/preview-sizes.png written")
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "variants" {
		vs := logo.Variants()
		for i, v := range vs {
			name := fmt.Sprintf("assets/variant-%d.png", i+1)
			os.WriteFile(name, logo.PNG(v.Draw(256)), 0o644)
			fmt.Println(name, "-", v.Name)
		}
		os.WriteFile("assets/variants.png", logo.PNG(logo.Sheet(vs, 256)), 0o644)
		fmt.Println("assets/variants.png written")
		return
	}
	sizes := []int{16, 20, 24, 32, 40, 48, 64, 128, 256}
	imgs := make([]*image.NRGBA, 0, len(sizes))
	for _, s := range sizes {
		imgs = append(imgs, logo.App(s))
	}
	if err := os.WriteFile("assets/app.ico", logo.ICO(imgs...), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile("assets/logo.png", logo.PNG(logo.App(512)), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if iconutilPath, err := exec.LookPath("iconutil"); err == nil {
		tmpDir, err := os.MkdirTemp("", "hy2tun_icons_*.iconset")
		if err == nil {
			defer os.RemoveAll(tmpDir)
			sizes := []struct {
				name string
				size int
			}{
				{"icon_16x16.png", 16},
				{"icon_16x16@2x.png", 32},
				{"icon_32x32.png", 32},
				{"icon_32x32@2x.png", 64},
				{"icon_128x128.png", 128},
				{"icon_128x128@2x.png", 256},
				{"icon_256x256.png", 256},
				{"icon_256x256@2x.png", 512},
				{"icon_512x512.png", 512},
				{"icon_512x512@2x.png", 1024},
			}
			for _, s := range sizes {
				os.WriteFile(filepath.Join(tmpDir, s.name), logo.PNG(logo.App(s.size)), 0o644)
			}
			exec.Command(iconutilPath, "-c", "icns", tmpDir, "-o", "assets/AppIcon.icns").Run()
		}
	}
	fmt.Println("assets/app.ico, assets/logo.png and assets/AppIcon.icns written")
}
