package chatmedia

import (
	"bytes"
	"fmt"
	"image/jpeg"
	"image/png"
)

// Source limits bound the decode before making a smaller, durable chat copy.
const MaxSourceBytes = 32 << 20
const maxSourcePixels = 64 << 20
const maxSourceSide = 32768

func prepareAttachment(name string, data []byte) (Ref, []byte, error) {
	ref, img, err := decodeImage(name, data, true)
	if err != nil {
		return Ref{}, nil, err
	}
	side := max(ref.Width, ref.Height)
	if side <= maxSide && len(data) <= MaxBytes {
		return ref, data, nil
	}
	// Byte-heavy screenshots may need a second reduction after fitting the dimensions.
	for {
		if side > maxSide {
			side = maxSide
		} else {
			side = max(1, side*3/4)
		}
		img = Thumbnail(img, side, side)
		var out bytes.Buffer
		if ref.Format == "jpeg" {
			err = jpeg.Encode(&out, img, &jpeg.Options{Quality: 88})
		} else {
			err = png.Encode(&out, img)
		}
		if err != nil {
			return Ref{}, nil, fmt.Errorf("encode resized attachment: %w", err)
		}
		if out.Len() <= MaxBytes {
			ref.Width, ref.Height = img.Bounds().Dx(), img.Bounds().Dy()
			return ref, out.Bytes(), nil
		}
	}
}
