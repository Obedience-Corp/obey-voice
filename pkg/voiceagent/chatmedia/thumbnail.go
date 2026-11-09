package chatmedia

import (
	"image"
	"image/color"
)

// Thumbnail fits an image inside w by h without enlarging it. Each output pixel
// averages its source area so thin lines and screenshot text do not disappear
// between samples. RGBA values stay premultiplied while filtering transparency.
func Thumbnail(src image.Image, w, h int) image.Image {
	b := src.Bounds()
	if b.Empty() {
		return image.NewRGBA(image.Rect(0, 0, 1, 1))
	}
	w = max(1, min(w, b.Dx()))
	h = max(1, min(h, max(1, b.Dy()*w/b.Dx())))
	w = max(1, min(w, b.Dx()*h/b.Dy()))
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		y0, y1 := b.Min.Y+y*b.Dy()/h, b.Min.Y+(y+1)*b.Dy()/h
		for x := 0; x < w; x++ {
			x0, x1 := b.Min.X+x*b.Dx()/w, b.Min.X+(x+1)*b.Dx()/w
			var r, g, blue, a uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					rr, gg, bb, aa := src.At(sx, sy).RGBA()
					r += uint64(rr)
					g += uint64(gg)
					blue += uint64(bb)
					a += uint64(aa)
				}
			}
			n := uint64((x1 - x0) * (y1 - y0))
			out.SetRGBA(x, y, color.RGBA{uint8((r / n) >> 8), uint8((g / n) >> 8), uint8((blue / n) >> 8), uint8((a / n) >> 8)})
		}
	}
	return out
}
