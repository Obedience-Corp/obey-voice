package chatmedia

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/color"
	"image/gif"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Small delta rectangles on a large logical canvas are valid GIFs and remain
// tiny on disk. Their decoded rectangle sizes do not bound preview work.
func deltaGIF(t testing.TB, width, height, frames int) []byte {
	t.Helper()
	palette := color.Palette{color.Black, color.White}
	g := &gif.GIF{Config: image.Config{ColorModel: palette, Width: width, Height: height}}
	for range frames {
		g.Image = append(g.Image, image.NewPaletted(image.Rect(0, 0, 1, 1), palette))
		g.Delay = append(g.Delay, 1)
	}
	var data bytes.Buffer
	if err := gif.EncodeAll(&data, g); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestGIFLogicalCanvasBudget(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		width, height, frames int
		accepted              bool
	}{
		{"maximum still", 4096, 4096, 1, true},
		{"exact animation budget", 512, 512, 64, true},
		{"over animation budget", 513, 512, 64, false},
		{"large canvas deltas", 4096, 4096, 64, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := deltaGIF(t, tc.width, tc.height, tc.frames)
			if _, err := gif.DecodeAll(bytes.NewReader(data)); err != nil {
				t.Fatalf("fixture is not a valid GIF: %v", err)
			}
			ref, err := Inspect("delta.gif", data)
			if tc.accepted {
				if err != nil || ref.Frames != tc.frames {
					t.Fatalf("in-budget GIF rejected: ref=%+v err=%v", ref, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "canvas work") {
				t.Fatalf("over-budget GIF must report canvas limit: %v", err)
			}
		})
	}
}

func TestGIFCanvasBudgetProtectsImportsAndStoredPreviews(t *testing.T) {
	data := deltaGIF(t, 4096, 4096, 64)
	store := Store{Dir: t.TempDir()}
	if _, err := store.Import("delta.gif", data); err == nil {
		t.Fatal("over-budget paste accepted")
	}
	work := t.TempDir()
	source := filepath.Join(work, "delta.gif")
	if err := os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	text, refs, err := store.ImportReply("![Animation](<"+source+">)", work)
	if err == nil || len(refs) != 0 || !strings.Contains(text, "Image unavailable") {
		t.Fatalf("over-budget worker result accepted: text=%q refs=%v err=%v", text, refs, err)
	}
	if entries, err := os.ReadDir(store.Dir); err != nil || len(entries) != 0 {
		t.Fatalf("rejected imports wrote attachments: entries=%v err=%v", entries, err)
	}
	// A previously accepted attachment must be checked again before rendering.
	sum := sha256.Sum256(data)
	ref := Ref{ID: hex.EncodeToString(sum[:]), Name: "delta.gif", Format: "gif", Width: 4096, Height: 4096, Frames: 64}
	if err := os.WriteFile(store.Path(ref), data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Preview(ref); err == nil || !strings.Contains(err.Error(), "canvas work") {
		t.Fatalf("stored over-budget GIF reached preview: %v", err)
	}
}

func BenchmarkInspectLargeCanvasDeltaGIF(b *testing.B) {
	data := deltaGIF(b, 4096, 4096, 64)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if _, err := Inspect("delta.gif", data); err == nil {
			b.Fatal("over-budget GIF accepted")
		}
	}
}
