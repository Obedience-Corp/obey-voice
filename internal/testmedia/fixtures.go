// Package testmedia provides small, deterministic images for isolated UI proofs.
package testmedia

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/png"
)

func PNG() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 120, 72))
	for y := 0; y < 72; y++ {
		for x := 0; x < 120; x++ {
			c := color.RGBA{20, 35, 65, 255}
			if x > 12 && x < 50 && y > 12 && y < 60 {
				c = color.RGBA{80, 210, 170, 255}
			}
			if x > 65 && x < 108 && y > 12 && y < 60 {
				c = color.RGBA{250, 185, 65, 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	var out bytes.Buffer
	_ = png.Encode(&out, img)
	return out.Bytes()
}
func GIF() []byte {
	palette := color.Palette{color.RGBA{20, 35, 65, 255}, color.RGBA{255, 100, 180, 255}, color.RGBA{100, 210, 255, 255}}
	g := &gif.GIF{LoopCount: 0}
	for i := 0; i < 6; i++ {
		img := image.NewPaletted(image.Rect(0, 0, 120, 72), palette)
		for y := 15; y < 57; y++ {
			for x := 10 + i*14; x < 30+i*14; x++ {
				img.SetColorIndex(x, y, uint8(1+i%2))
			}
		}
		g.Image = append(g.Image, img)
		g.Delay = append(g.Delay, 15)
	}
	var out bytes.Buffer
	_ = gif.EncodeAll(&out, g)
	return out.Bytes()
}
