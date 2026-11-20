package chatmedia

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLargePhotoReplyAndScreenshotDrop(t *testing.T) {
	for _, format := range []string{"jpeg", "png"} {
		t.Run(format, func(t *testing.T) {
			root := t.TempDir()
			store := Store{Dir: filepath.Join(root, "store")}
			img := image.NewNRGBA(image.Rect(0, 0, 5120, 80))
			for x := range 5120 {
				img.SetNRGBA(x, 0, color.NRGBA{R: 220, A: 128})
			}
			var source bytes.Buffer
			var err error
			if format == "jpeg" {
				err = jpeg.Encode(&source, img, nil)
			} else {
				err = png.Encode(&source, img)
			}
			if err != nil {
				t.Fatal(err)
			}
			name := "large." + format
			path := filepath.Join(root, name)
			if err := os.WriteFile(path, source.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			reply := "![Photo](" + name + ")"
			if err := store.CheckReplyFiles(reply, root); err != nil {
				t.Fatal(err)
			}
			_, refs, err := store.ImportReply(reply, root)
			if err != nil || len(refs) != 1 {
				t.Fatalf("reply: %v %v", refs, err)
			}
			dropped, err := store.ImportFile(path)
			if err != nil || dropped != refs[0] {
				t.Fatalf("drop: %+v %v", dropped, err)
			}
			data, err := store.Read(dropped)
			if err != nil {
				t.Fatal(err)
			}
			decoded, gotFormat, err := image.Decode(bytes.NewReader(data))
			if err != nil || gotFormat != format || decoded.Bounds() != image.Rect(0, 0, 4096, 64) || dropped.Width != 4096 || dropped.Height != 64 || len(data) > MaxBytes {
				t.Fatalf("stored copy: %+v, %s, %v", dropped, gotFormat, err)
			}
			if format == "png" {
				_, _, _, a := decoded.At(0, 0).RGBA()
				if a == 0 || a == 65535 {
					t.Fatal("resized PNG lost transparency")
				}
			}
			original, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(source.Bytes(), original) {
				t.Fatal("source file changed")
			}
		})
	}
}

func TestByteHeavyScreenshotFitsStore(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 1600, 1600))
	_, _ = rand.New(rand.NewSource(1)).Read(img.Pix)
	var src bytes.Buffer
	if err := png.Encode(&src, img); err != nil {
		t.Fatal(err)
	}
	if src.Len() <= MaxBytes {
		t.Fatal("fixture must exceed byte limit")
	}
	ref, data, err := prepareAttachment("screenshot.png", src.Bytes())
	if err != nil || len(data) > MaxBytes || ref.Width >= 1600 || ref.Height != ref.Width {
		t.Fatalf("resized: %+v %d bytes, %v", ref, len(data), err)
	}
}

func TestSourcePixelLimitBeforeDecode(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()
	binary.BigEndian.PutUint32(data[16:20], 16384)
	binary.BigEndian.PutUint32(data[20:24], 8192)
	binary.BigEndian.PutUint32(data[29:33], crc32.ChecksumIEEE(data[12:29]))
	_, _, err := prepareAttachment("oversized.png", data)
	if err == nil || !strings.Contains(err.Error(), "16384x8192") {
		t.Fatalf("expected dimension limit before pixel decode, got %v", err)
	}
}
