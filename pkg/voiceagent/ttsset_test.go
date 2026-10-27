package voiceagent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

// Conversation path may attach Kokoro as fallback when primary is qwen3-tts.
// Preview/batch/narrate must call tts.NewProvider directly (fail closed) so a
// missing or broken Qwen never silently produces a Kokoro voice mid-book.
func TestNewTTSProviderSetConversationFallback(t *testing.T) {
	// Use a fake absolute "binary" that is the test executable so LookPath works
	// for the external CLI path without starting a real model.
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	modelDir := t.TempDir()
	// Create a non-qwen3-tts-worker name so we take the CLI adapter path.
	// Factory needs a resolvable binary for qwen3-tts.
	bin := filepath.Join(t.TempDir(), "qwen3-tts-cli")
	if err := os.Symlink(exe, bin); err != nil {
		// Windows or symlink failure: copy not needed if we skip
		if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	cfg := &config.Config{
		TTSProvider:         "qwen3-tts",
		TTSFallbackProvider: "kokoro",
		QwenTTSBinary:       bin,
		QwenTTSModel:        modelDir,
	}
	set, err := NewTTSSet(cfg)
	if err != nil {
		// Kokoro may also fail if models missing — primary must still construct for CLI path.
		// If primary fails, that's a different bug.
		t.Fatalf("NewTTSSet: %v", err)
	}
	defer set.Close()
	if set.Primary == nil {
		t.Fatal("primary required")
	}
	// Fallback may be nil if Kokoro assets are not installed in this environment;
	// the warning must then be set (fail soft on fallback construction only).
	if set.Fallback == nil && set.FallbackWarning == nil {
		t.Fatal("expected either Kokoro fallback or a FallbackWarning when assets missing")
	}
	if set.FallbackWarning != nil && !strings.Contains(set.FallbackWarning.Error(), "fallback") {
		t.Fatalf("unexpected warning: %v", set.FallbackWarning)
	}
}

func TestNewTTSProviderSetNoFallbackWhenPrimaryIsKokoro(t *testing.T) {
	// When primary is already Kokoro, do not double-attach a fallback set.
	cfg := &config.Config{
		TTSProvider:         "kokoro",
		TTSFallbackProvider: "kokoro",
	}
	// Construction may fail without Kokoro models — only assert policy when it succeeds.
	set, err := NewTTSSet(cfg)
	if err != nil {
		t.Skipf("kokoro not installable in this environment: %v", err)
	}
	defer set.Close()
	if set.Fallback != nil {
		t.Fatal("fallback must be nil when primary is already kokoro")
	}
}

func TestLiveTTSManagerRetiresPreviousSet(t *testing.T) {
	p := &pipeline.Pipeline{}
	m := &LiveTTSManager{RetireDelay: -1}
	t.Cleanup(m.Close)

	var closed atomic.Bool
	first := &TTSSet{cleanups: []func(){func() { closed.Store(true) }}}
	if !m.Adopt(first) {
		t.Fatal("Adopt initial set")
	}
	if !m.Install(p, &TTSSet{}) {
		t.Fatal("Install replacement")
	}
	if !closed.Load() {
		t.Fatal("replaced set was not closed")
	}
}

func TestLiveTTSManagerRetiresAfterDelay(t *testing.T) {
	p := &pipeline.Pipeline{}
	m := &LiveTTSManager{RetireDelay: 20 * time.Millisecond}
	t.Cleanup(m.Close)

	var closed atomic.Bool
	first := &TTSSet{cleanups: []func(){func() { closed.Store(true) }}}
	if !m.Adopt(first) {
		t.Fatal("Adopt initial set")
	}
	if !m.Install(p, &TTSSet{}) {
		t.Fatal("Install replacement")
	}
	if closed.Load() {
		t.Fatal("replaced set closed before the drain")
	}
	deadline := time.Now().Add(time.Second)
	for !closed.Load() {
		if time.Now().After(deadline) {
			t.Fatal("replaced set was not closed after the drain")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestLiveTTSManagerCloseAbortsRetire(t *testing.T) {
	p := &pipeline.Pipeline{}
	m := &LiveTTSManager{RetireDelay: time.Hour}

	var closed atomic.Bool
	first := &TTSSet{cleanups: []func(){func() { closed.Store(true) }}}
	if !m.Adopt(first) {
		t.Fatal("Adopt initial set")
	}
	if !m.Install(p, &TTSSet{}) {
		t.Fatal("Install replacement")
	}
	m.Close()
	if !closed.Load() {
		t.Fatal("Close left the replaced set loaded")
	}
	if m.Install(p, &TTSSet{}) {
		t.Fatal("Install after Close must fail")
	}
}

func TestLiveTTSManagerReplaceAndCloseIgnoresDrain(t *testing.T) {
	p := &pipeline.Pipeline{}
	m := &LiveTTSManager{RetireDelay: time.Hour}
	t.Cleanup(m.Close)

	var closed atomic.Bool
	first := &TTSSet{cleanups: []func(){func() { closed.Store(true) }}}
	if !m.Adopt(first) {
		t.Fatal("Adopt initial set")
	}
	if !m.ReplaceAndClose(p, &TTSSet{}) {
		t.Fatal("ReplaceAndClose")
	}
	if !closed.Load() {
		t.Fatal("persona-switch path left the previous worker loaded")
	}
}

type transactionTTS struct{}

type retainedTestTTS struct {
	transactionTTS
	name string
}

func TestLiveTTSManagerWaitsForWorkerVoiceAndQueuedSpeech(t *testing.T) {
	old := &retainedTestTTS{name: "Obey"}
	next := &retainedTestTTS{name: "Samantha"}
	p := &pipeline.Pipeline{TTS: old}
	m := &LiveTTSManager{RetireDelay: -1}
	defer m.Close()
	closed := make(chan struct{})
	m.Adopt(&TTSSet{Primary: old, cleanups: []func(){func() { close(closed) }}})
	releaseWorker := p.RetainTTS(old, nil)
	if !m.ReplaceAndClose(p, &TTSSet{Primary: next}) {
		t.Fatal("replace voice")
	}
	select {
	case <-closed:
		t.Fatal("worker's voice closed during persona switch")
	default:
	}
	releaseSpeech := p.RetainTTS(old, nil)
	releaseWorker()
	select {
	case <-closed:
		t.Fatal("queued speech lost its voice")
	default:
	}
	releaseSpeech()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("unused voice was not retired")
	}
}

func TestLiveTTSManagerShutdownClosesRetainedWorkerVoice(t *testing.T) {
	old := &retainedTestTTS{name: "Obey"}
	p := &pipeline.Pipeline{TTS: old}
	m := &LiveTTSManager{RetireDelay: -1}
	closed := make(chan struct{})
	m.Adopt(&TTSSet{Primary: old, cleanups: []func(){func() { close(closed) }}})
	release := p.RetainTTS(old, nil)
	defer release()
	m.ReplaceAndClose(p, &TTSSet{Primary: &retainedTestTTS{name: "Samantha"}})
	m.Close()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("shutdown left retained voice running")
	}
}

func (transactionTTS) Synthesize(ctx context.Context, _ string) (*audio.PCMStream, error) {
	stream := audio.NewPCMStream(ctx)
	stream.Close()
	return stream, nil
}
func (transactionTTS) Available() bool                       { return true }
func (transactionTTS) ListVoices(string, string) []tts.Voice { return nil }

func TestLiveTTSManagerReplaceTransactionPublishesOnlyAfterCommit(t *testing.T) {
	p := &pipeline.Pipeline{}
	m := &LiveTTSManager{RetireDelay: -1}
	oldProvider := transactionTTS{}
	newProvider := transactionTTS{}
	oldSet := &TTSSet{Primary: oldProvider}
	newSet := &TTSSet{Primary: newProvider}
	if !m.Install(p, oldSet) {
		t.Fatal("initial Install")
	}
	wantErr := errors.New("persist failed")
	called := false
	if err := m.ReplaceTransaction(p, newSet, func() error { called = true; return wantErr }); !errors.Is(err, wantErr) {
		t.Fatalf("ReplaceTransaction error = %v, want %v", err, wantErr)
	}
	if !called {
		t.Fatal("transaction callback was not called")
	}
	if p.TTS != oldProvider {
		t.Fatal("voice changed after failed transaction")
	}

	if err := m.ReplaceTransaction(p, newSet, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if p.TTS != newProvider {
		t.Fatal("voice was not published after successful transaction")
	}
}

func TestLiveTTSManagerReplaceTransactionRejectsClosedBeforeCallback(t *testing.T) {
	p := &pipeline.Pipeline{}
	m := &LiveTTSManager{RetireDelay: -1}
	oldProvider := transactionTTS{}
	newProvider := transactionTTS{}
	if !m.Install(p, &TTSSet{Primary: oldProvider}) {
		t.Fatal("initial Install")
	}
	m.Close()
	called := false
	if err := m.ReplaceTransaction(p, &TTSSet{Primary: newProvider}, func() error { called = true; return nil }); err == nil {
		t.Fatal("closed manager accepted transaction")
	}
	if called {
		t.Fatal("closed manager ran transaction callback")
	}
	if p.TTS != oldProvider {
		t.Fatal("closed manager changed voice")
	}
}

func TestLiveTTSManagerSameSetRemainsUsable(t *testing.T) {
	p := &pipeline.Pipeline{}
	m := &LiveTTSManager{RetireDelay: -1}
	var closed atomic.Int32
	set := &TTSSet{Primary: transactionTTS{}, cleanups: []func(){func() { closed.Add(1) }}}
	if !m.Install(p, set) {
		t.Fatal("install")
	}
	if err := m.ReplaceTransaction(p, set, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if closed.Load() != 0 {
		t.Fatal("same-set transaction closed live voice")
	}
	m.Close()
	if closed.Load() != 1 {
		t.Fatal("voice not closed exactly once on shutdown")
	}
}
