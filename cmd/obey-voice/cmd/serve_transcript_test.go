//go:build !integration

package cmd

import (
	"context"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
)

// sessionOnlyTestBrain keeps its own unattributed history, as the Claude,
// Grok and Cursor CLI sessions do.
type sessionOnlyTestBrain struct{ history []brain.Turn }

func (*sessionOnlyTestBrain) ThinkStream(context.Context, string, brain.StreamOptions) (*brain.Stream, error) {
	return nil, nil
}
func (*sessionOnlyTestBrain) ThinkFull(context.Context, string, brain.StreamOptions) (string, error) {
	return "", nil
}
func (b *sessionOnlyTestBrain) ClearHistory()         { b.history = nil }
func (b *sessionOnlyTestBrain) History() []brain.Turn { return append([]brain.Turn(nil), b.history...) }
func (b *sessionOnlyTestBrain) LoadHistory(turns []brain.Turn) {
	b.history = nil
	for _, t := range turns {
		b.history = append(b.history, brain.Turn{Role: t.Role, Content: t.Content})
	}
}

func TestServeResumeRestoresTranscriptForProviderWithoutSharedHistory(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	writeAckTestPersona(t, "mira", "You are Mira.")
	cfg := &config.Config{ActivePersona: "mira", BrainProvider: "claude", TTSProvider: "kokoro"}
	manager := &voiceagent.LiveTTSManager{}
	t.Cleanup(manager.Close)
	s := &servePersonaSwitcher{cfg: cfg, pipeline: &pipeline.Pipeline{Brain: &sessionOnlyTestBrain{}}, tts: manager,
		sessions: &sessionRef{sess: session.New("claude", "")},
		newBrain: func(*config.Config) (brain.Provider, error) { return &sessionOnlyTestBrain{}, nil },
		newTTS: func(*config.Config) (*voiceagent.TTSSet, error) {
			return &voiceagent.TTSSet{Primary: &fakeTTS{sampleRate: 24000, samples: 64}}, nil
		},
	}
	defer func() { _ = s.pipeline.CloseBrains() }()
	if err := s.initialize(); err != nil {
		t.Fatal(err)
	}
	restored := session.New("claude", "")
	turns := []brain.Turn{
		{Role: "user", Content: "The shared code is cobalt."},
		{Role: "assistant", Content: "Remembered.", PersonaID: "mira", PersonaDisplayName: "mira", ResponseID: "saved-response"},
	}
	if err := session.DefaultStore().SaveSelection(restored, turns, "mira", "mira", "saved-hash"); err != nil {
		t.Fatal(err)
	}
	if err := s.resume(context.Background(), restored.ID); err != nil {
		t.Fatal(err)
	}
	if got := s.selector.Snapshot(); got.SessionID != restored.ID || got.PersonaID != "mira" {
		t.Fatalf("resumed selection = %+v", got)
	}
	if own := s.pipeline.Brain.History(); len(own) != 2 || own[1].PersonaID != "" {
		t.Fatalf("provider projection = %+v", own)
	}
	if err := s.sessions.save(s.sessions.history(s.pipeline)); err != nil {
		t.Fatal(err)
	}
	disk, err := session.Load(restored.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(disk.Turns) != 2 || disk.Turns[1].PersonaID != "mira" || disk.Turns[1].ResponseID != "saved-response" {
		t.Fatalf("saved transcript lost attribution: %+v", disk.Turns)
	}
}
