package chatmedia

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/testmedia"
)

func TestDurableImportAndPreview(t *testing.T) {
	store := Store{Dir: filepath.Join(t.TempDir(), "assets")}
	for _, tc := range []struct {
		name   string
		data   []byte
		frames int
	}{{"Screenshot with spaces.png", testmedia.PNG(), 1}, {"motion.gif", testmedia.GIF(), 6}} {
		path := filepath.Join(t.TempDir(), tc.name)
		if err := os.WriteFile(path, tc.data, 0600); err != nil {
			t.Fatal(err)
		}
		ref, err := store.ImportFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.Remove(path); err != nil {
			t.Fatal(err)
		}
		got, err := store.Read(ref)
		if err != nil || !bytes.Equal(got, tc.data) {
			t.Fatalf("source deletion lost attachment: %v", err)
		}
		if ref.Frames != tc.frames {
			t.Fatalf("frames=%d", ref.Frames)
		}
		p, err := store.Preview(ref)
		if err != nil || len(p.Frames) != tc.frames {
			t.Fatalf("preview: %v", err)
		}
		if ref.Frames > 1 && p.Frame(0, false) == p.Frame(200*time.Millisecond, false) {
			t.Fatal("animation did not advance")
		}
		if p.Frame(0, true) != p.Frame(time.Second, true) {
			t.Fatal("paused GIF changed frame")
		}
		model, err := store.ModelImage(ref)
		if err != nil {
			t.Fatal(err)
		}
		_, format, err := image.Decode(bytes.NewReader(model))
		if err != nil || format != "png" {
			t.Fatalf("model frame: %s %v", format, err)
		}
	}
}
func TestRejectsInvalidAndExcessiveImages(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	for _, data := range [][]byte{nil, []byte("not an image"), make([]byte, MaxBytes+1), testmedia.PNG()[:40]} {
		if _, err := store.Import("bad.png", data); err == nil {
			t.Fatal("accepted invalid image")
		}
	}
	g := &gif.GIF{}
	for range 65 {
		g.Image = append(g.Image, image.NewPaletted(image.Rect(0, 0, 1, 1), color.Palette{color.Black, color.White}))
		g.Delay = append(g.Delay, 1)
	}
	var b bytes.Buffer
	_ = gif.EncodeAll(&b, g)
	if _, err := store.Import("too-many.gif", b.Bytes()); err == nil {
		t.Fatal("accepted excessive GIF frames")
	}
	if _, err := store.Read(Ref{ID: "../../secret"}); err == nil {
		t.Fatal("accepted path traversal")
	}
	r, err := store.Import("good.png", testmedia.PNG())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(store.Path(r), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Read(r); err == nil {
		t.Fatal("accepted tampered asset")
	}
}
func TestPastedPaths(t *testing.T) {
	for input, want := range map[string]string{`/tmp/Screenshot at 02.33.png`: "/tmp/Screenshot at 02.33.png", `/tmp/Screenshot\ at\ 02.33.png`: "/tmp/Screenshot at 02.33.png", `"/tmp/screenshot image.png"`: "/tmp/screenshot image.png", `file:///tmp/a%20b.gif`: "/tmp/a b.gif"} {
		paths, ok := Paths(input)
		if !ok || len(paths) != 1 || paths[0] != want {
			t.Fatalf("%q => %v %v", input, paths, ok)
		}
	}
	for _, text := range []string{"What is image.png?", "hello", "/help", "https://example.com/a.png", "/tmp/a.txt"} {
		if _, ok := Paths(text); ok {
			t.Fatalf("treated ordinary paste as image: %q", text)
		}
	}
}
