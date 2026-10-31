package chatmedia

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
)

func TestRetainedAudioPublicationAndValidation(t *testing.T) {
	root := t.TempDir()
	store := Store{Dir: root}
	ctx := context.Background()
	ref, data, err := CanonicalAudio(audioFixture(4))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PersistAudio(ctx, ref, data); err != nil {
		t.Fatal(err)
	}
	if err := store.PersistAudio(ctx, ref, data); err != nil {
		t.Fatal("idempotent publish", err)
	}
	loaded, err := store.ReadAudio(ctx, ref)
	if err != nil || !bytes.Equal(loaded, data) {
		t.Fatal(err)
	}
	st, err := os.Stat(store.AudioPath(ref))
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatal(st, err)
	}
	if err := os.WriteFile(store.AudioPath(ref), []byte("damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadAudio(ctx, ref); !errors.Is(err, utterance.ErrInvalidAudio) {
		t.Fatal(err)
	}
	if err := store.PersistAudio(ctx, ref, data); !errors.Is(err, utterance.ErrInvalidAudio) {
		t.Fatal("overwrote corrupt immutable asset", err)
	}
}
func TestRetainedAudioRejectsCancellationAndSymlink(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := Store{Dir: root}
	ref, data, err := CanonicalAudio(audioFixture(4))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.PersistAudio(ctx, ref, data); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "audio")); err != nil {
		t.Fatal(err)
	}
	if err := store.PersistAudio(context.Background(), ref, data); err == nil {
		t.Fatal("followed symlink")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatal(entries, err)
	}
}

func TestRetainedAudioRequiresExplicitSessionRestore(t *testing.T) {
	store := Store{Dir: t.TempDir()}
	ctx := context.Background()
	original := NewAudioSession()
	ref, err := original.ImportSamples(ctx, []float32{0.1, 0.2})
	if err != nil {
		t.Fatal(err)
	}
	if err := original.PersistAudio(ctx, store, ref); err != nil {
		t.Fatal(err)
	}
	original.Close()
	restored := NewAudioSession()
	defer restored.Close()
	if _, err := restored.ResolveAudio(ctx, ref); !errors.Is(err, utterance.ErrAudioUnavailable) {
		t.Fatal("implicit disk fallback", err)
	}
	if err := restored.RestoreAudio(ctx, store, ref); err != nil {
		t.Fatal(err)
	}
	if _, err := restored.ResolveAudio(ctx, ref); err != nil {
		t.Fatal(err)
	}
}
