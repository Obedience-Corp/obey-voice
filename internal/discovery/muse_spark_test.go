package discovery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Obedience-Corp/muse-spark-go/pkg/muse"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

// museOnPath puts an inert muse on PATH and replaces the catalog read.
func museOnPath(t *testing.T, catalog func(context.Context) ([]string, error)) *int {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "muse"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	calls := new(int)
	stub := museCatalog
	museCatalog = func(ctx context.Context, _ *muse.Client) ([]string, error) {
		*calls++
		return catalog(ctx)
	}
	t.Cleanup(func() { museCatalog = stub })
	return calls
}

func TestMuseSparkListsTheCatalog(t *testing.T) {
	museOnPath(t, func(context.Context) ([]string, error) { return []string{"model-b", "model-a"}, nil })
	for saved, want := range map[string][]string{
		"":         {"default", "model-b", "model-a"},
		"model-a":  {"default", "model-b", "model-a"},
		"retired":  {"default", "model-b", "model-a", "retired"},
		" DEFAULT": {"default", "model-b", "model-a"},
	} {
		info := DiscoverProvider(t.Context(), &config.Config{MuseSparkModel: saved}, "muse-spark")
		if !info.Available || info.ModelsPending || !slices.Equal(info.Models, want) {
			t.Errorf("saved %q: %+v, want models %q", saved, info, want)
		}
	}
}

func TestMuseSparkFallsBackWhenTheCatalogFails(t *testing.T) {
	museOnPath(t, func(context.Context) ([]string, error) { return nil, errors.New("method not found") })
	info := DiscoverProvider(t.Context(), &config.Config{MuseSparkModel: "chosen"}, "muse-spark")
	if !info.Available || !slices.Equal(info.Models, []string{"default", "chosen"}) {
		t.Fatalf("got %+v", info)
	}
}

func TestMuseSparkCatalogReadIsBounded(t *testing.T) {
	prev := probeTimeout
	probeTimeout = 50 * time.Millisecond
	t.Cleanup(func() { probeTimeout = prev })
	museOnPath(t, func(ctx context.Context) ([]string, error) { <-ctx.Done(); return nil, ctx.Err() })
	start := time.Now()
	info := DiscoverProvider(t.Context(), &config.Config{}, "muse-spark")
	if time.Since(start) > time.Second || !slices.Equal(info.Models, []string{"default"}) {
		t.Fatalf("got %+v after %s", info, time.Since(start))
	}
}

func TestMuseSparkWithoutMuse(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	info := DiscoverProvider(t.Context(), &config.Config{MuseSparkModel: "chosen"}, "muse-spark")
	if info.Available || !slices.Equal(info.Models, []string{"default", "chosen"}) {
		t.Fatalf("got %+v", info)
	}
	for _, p := range DiscoverProvidersStartup(&config.Config{OllamaHost: "http://127.0.0.1:1"}) {
		if p.Name == "muse-spark" && p.ModelsPending {
			t.Fatalf("unavailable muse-spark marked pending: %+v", p)
		}
	}
}

func TestStartupScanDoesNotStartMuse(t *testing.T) {
	calls := museOnPath(t, func(context.Context) ([]string, error) { return []string{"model-a"}, nil })
	var info ProviderInfo
	for _, p := range DiscoverProvidersStartup(&config.Config{MuseSparkModel: "chosen", OllamaHost: "http://127.0.0.1:1"}) {
		if p.Name == "muse-spark" {
			info = p
		}
	}
	if !info.Available || !info.ModelsPending || !slices.Equal(info.Models, []string{"default", "chosen"}) {
		t.Fatalf("startup muse-spark = %+v", info)
	}
	if *calls != 0 {
		t.Fatalf("startup read the catalog %d times", *calls)
	}
}
