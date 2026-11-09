package chatmedia

import (
	"image"
	"image/color"
	"testing"
)

func TestThumbnailPreservesThinDetail(t *testing.T) {
	// A nearest-neighbor preview drops every white column in this fixture.
	src := image.NewRGBA(image.Rect(10, 20, 26, 36))
	for y := 20; y < 36; y++ {
		for x := 10; x < 26; x++ {
			c := color.RGBA{A: 255}
			if x%2 == 1 {
				c = color.RGBA{255, 255, 255, 255}
			}
			src.SetRGBA(x, y, c)
		}
	}
	got := Thumbnail(src, 4, 4)
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			r, g, b, a := got.At(x, y).RGBA()
			if r>>8 != 127 || g != r || b != r || a != 65535 {
				t.Fatalf("thin detail lost at %d,%d: %v", x, y, got.At(x, y))
			}
		}
	}
}

func TestThumbnailFitsWithoutUpscaling(t *testing.T) {
	for _, tc := range []struct{ sw, sh, w, h, wantW, wantH int }{
		{100, 200, 40, 40, 20, 40},
		{200, 100, 40, 40, 40, 20},
		{2, 2, 40, 40, 2, 2},
		{1, 100, 1, 2, 1, 2},
	} {
		got := Thumbnail(image.NewRGBA(image.Rect(0, 0, tc.sw, tc.sh)), tc.w, tc.h).Bounds()
		if got.Dx() != tc.wantW || got.Dy() != tc.wantH {
			t.Fatalf("%+v: got %v", tc, got)
		}
	}
}
