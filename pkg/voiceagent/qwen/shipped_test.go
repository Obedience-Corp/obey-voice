package qwen

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLookupShippedVeronica(t *testing.T) {
	v, ok := LookupShipped("veronica")
	if !ok || v.Name != "Veronica" {
		t.Fatalf("LookupShipped(veronica) = %+v %v", v, ok)
	}
	if !strings.Contains(strings.ToLower(v.RefText), "rhythm of my voice") {
		t.Fatalf("Veronica ref text = %q, want the booth line", v.RefText)
	}
	if len(v.wav) < 1000 {
		t.Fatalf("embedded Veronica wav is %d bytes, want the booth clip", len(v.wav))
	}
}

func TestLookupShippedVeronicaDesk(t *testing.T) {
	booth, ok := LookupShipped("veronica")
	if !ok {
		t.Fatal("missing Veronica")
	}
	for _, key := range []string{"VeronicaDesk", "veronica-desk", "veronica desk", "veronicadesk"} {
		v, ok := LookupShipped(key)
		if !ok || v.Name != "VeronicaDesk" {
			t.Fatalf("LookupShipped(%q) = %+v %v", key, v, ok)
		}
		if v.ID == booth.ID || len(v.wav) == len(booth.wav) {
			t.Fatalf("%q collided with booth throat", key)
		}
		if !strings.Contains(strings.ToLower(v.RefText), "voice you actually talk to") {
			t.Fatalf("desk ref text = %q", v.RefText)
		}
	}
}

func TestResolveVoiceDistinguishesPresetAndClone(t *testing.T) {
	name, shipped, ok := ResolveVoice("serena")
	if !ok || shipped || name != "Serena" {
		t.Fatalf("Serena: %q shipped=%v ok=%v", name, shipped, ok)
	}
	name, shipped, ok = ResolveVoice("VERONICA")
	if !ok || !shipped || name != "Veronica" {
		t.Fatalf("Veronica: %q shipped=%v ok=%v", name, shipped, ok)
	}
	name, shipped, ok = ResolveVoice("veronica-desk")
	if !ok || !shipped || name != "VeronicaDesk" {
		t.Fatalf("VeronicaDesk: %q shipped=%v ok=%v", name, shipped, ok)
	}
	if _, _, ok := ResolveVoice("not-a-voice"); ok {
		t.Fatal("unknown voice resolved")
	}
}

func TestSelectableVoicesIncludesVeronica(t *testing.T) {
	var sawBooth, sawDesk bool
	for _, v := range SelectableVoices() {
		switch v.Name {
		case "Veronica":
			sawBooth = true
			if v.NativeLanguage != "English" {
				t.Fatalf("Veronica language = %q", v.NativeLanguage)
			}
		case "VeronicaDesk":
			sawDesk = true
		}
	}
	if !sawBooth {
		t.Fatal("SelectableVoices missing Veronica")
	}
	if !sawDesk {
		t.Fatal("SelectableVoices missing VeronicaDesk")
	}
	if len(SelectableVoices()) != len(CustomVoices())+2 {
		t.Fatalf("SelectableVoices = %d, want presets plus 2 shipped clones", len(SelectableVoices()))
	}
}

func TestMaterializeShippedWritesOnce(t *testing.T) {
	ctx := context.Background()
	v, ok := LookupShipped("Veronica")
	if !ok {
		t.Fatal("missing Veronica")
	}
	dir := t.TempDir()
	path, err := MaterializeShipped(ctx, v, dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(v.wav) {
		t.Fatalf("wrote %d bytes, embed is %d", len(got), len(v.wav))
	}
	again, err := MaterializeShipped(ctx, v, dir)
	if err != nil {
		t.Fatal(err)
	}
	if again != path {
		t.Fatalf("second materialize path %q, want %q", again, path)
	}
	if filepath.Base(path) != "ref.wav" {
		t.Fatalf("path %q", path)
	}
	desk, ok := LookupShipped("VeronicaDesk")
	if !ok {
		t.Fatal("missing VeronicaDesk")
	}
	deskPath, err := MaterializeShipped(ctx, desk, dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(deskPath) == filepath.Dir(path) {
		t.Fatalf("desk and booth materialized into the same dir %q", filepath.Dir(path))
	}
}

func TestMaterializeShippedHonoursCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	v, _ := LookupShipped("Veronica")
	if _, err := MaterializeShipped(ctx, v, t.TempDir()); err == nil {
		t.Fatal("want ctx error")
	}
}
