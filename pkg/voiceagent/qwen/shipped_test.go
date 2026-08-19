package qwen

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLookupShippedVeronica(t *testing.T) {
	v, ok := LookupShipped("veronica")
	if !ok || v.Name != "Veronica" {
		t.Fatalf("LookupShipped(veronica) = %+v %v", v, ok)
	}
	if v.RefText == "" {
		t.Fatal("Veronica ref text is empty")
	}
	if len(v.wav) < 1000 {
		t.Fatalf("embedded Veronica wav is %d bytes, want the Imagine clip", len(v.wav))
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
	if _, _, ok := ResolveVoice("not-a-voice"); ok {
		t.Fatal("unknown voice resolved")
	}
}

func TestSelectableVoicesIncludesVeronica(t *testing.T) {
	var saw bool
	for _, v := range SelectableVoices() {
		if v.Name == "Veronica" {
			saw = true
			if v.NativeLanguage != "English" {
				t.Fatalf("Veronica language = %q", v.NativeLanguage)
			}
		}
	}
	if !saw {
		t.Fatal("SelectableVoices missing Veronica")
	}
	if len(SelectableVoices()) <= len(CustomVoices()) {
		t.Fatal("SelectableVoices should be presets plus shipped clones")
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
}

func TestMaterializeShippedHonoursCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	v, _ := LookupShipped("Veronica")
	if _, err := MaterializeShipped(ctx, v, t.TempDir()); err == nil {
		t.Fatal("want ctx error")
	}
}
