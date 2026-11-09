package chatmedia

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/draw"
	"image/gif"
	"time"
)

// Inspect frame descriptors before DecodeAll can allocate a hostile animation.
func checkGIF(data []byte) (int, error) {
	bad := errors.New("invalid GIF or animation exceeds 64 frames / 16 million frame pixels")
	if len(data) < 13 {
		return 0, bad
	}
	pos := 13
	if data[10]&128 != 0 {
		pos += 3 * (1 << ((data[10] & 7) + 1))
	}
	frames, pixels := 0, int64(0)
	blocks := func() bool {
		for pos < len(data) {
			n := int(data[pos])
			pos++
			if n == 0 {
				return true
			}
			pos += n
		}
		return false
	}
	for pos < len(data) {
		tag := data[pos]
		pos++
		switch tag {
		case 0x3b:
			if frames > 0 {
				return frames, nil
			}
			return 0, bad
		case 0x21:
			pos++
			if !blocks() {
				return 0, bad
			}
		case 0x2c:
			if pos+9 > len(data) {
				return 0, bad
			}
			w := binary.LittleEndian.Uint16(data[pos+4:])
			h := binary.LittleEndian.Uint16(data[pos+6:])
			flags := data[pos+8]
			pos += 9
			frames++
			pixels += int64(w) * int64(h)
			if frames > 64 || pixels > MaxPixels {
				return 0, bad
			}
			if flags&128 != 0 {
				pos += 3 * (1 << ((flags & 7) + 1))
			}
			pos++
			if !blocks() {
				return 0, bad
			}
		default:
			return 0, bad
		}
	}
	return 0, bad
}

type Preview struct {
	Frames []image.Image
	Delays []time.Duration
}

func (s Store) Preview(r Ref) (Preview, error) {
	data, err := s.Read(r)
	if err != nil {
		return Preview{}, err
	}
	if _, err = Inspect(r.Name, data); err != nil {
		return Preview{}, err
	}
	if r.Format != "gif" {
		img, _, err := image.Decode(bytes.NewReader(data))
		return Preview{Frames: []image.Image{Thumbnail(img, 160, 100)}}, err
	}
	g, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil {
		return Preview{}, err
	}
	canvas := image.NewRGBA(image.Rect(0, 0, g.Config.Width, g.Config.Height))
	var p Preview
	for i, frame := range g.Image {
		before := image.NewRGBA(canvas.Bounds())
		draw.Draw(before, before.Bounds(), canvas, image.Point{}, draw.Src)
		draw.Draw(canvas, frame.Bounds(), frame, frame.Bounds().Min, draw.Over)
		// Keep only small preview frames, not full-resolution copies of every frame.
		p.Frames = append(p.Frames, Thumbnail(canvas, 160, 100))
		p.Delays = append(p.Delays, max(20*time.Millisecond, time.Duration(g.Delay[i])*10*time.Millisecond))
		if i < len(g.Disposal) {
			switch g.Disposal[i] {
			case gif.DisposalBackground:
				draw.Draw(canvas, frame.Bounds(), image.Transparent, image.Point{}, draw.Src)
			case gif.DisposalPrevious:
				canvas = before
			}
		}
	}
	return p, nil
}
func (p Preview) Frame(elapsed time.Duration, paused bool) image.Image {
	if len(p.Frames) == 0 {
		return nil
	}
	if paused || len(p.Delays) == 0 {
		return p.Frames[0]
	}
	var total time.Duration
	for _, d := range p.Delays {
		total += d
	}
	if total <= 0 {
		return p.Frames[0]
	}
	elapsed %= total
	for i, d := range p.Delays {
		if elapsed < d {
			return p.Frames[i]
		}
		elapsed -= d
	}
	return p.Frames[0]
}
