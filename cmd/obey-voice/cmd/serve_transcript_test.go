//go:build !integration

package cmd

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
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

func TestServeFailedResumeKeepsThePreviousConversation(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	writeAckTestPersona(t, "mira", "You are Mira.")
	cfg := &config.Config{ActivePersona: "mira", BrainProvider: "claude", TTSProvider: "kokoro", VoiceWorkEnabled: true}
	manager := &voiceagent.LiveTTSManager{}
	t.Cleanup(manager.Close)
	s := &servePersonaSwitcher{cfg: cfg, pipeline: &pipeline.Pipeline{Brain: &sessionOnlyTestBrain{}}, tts: manager,
		sessions: &sessionRef{sess: session.New("claude", "")},
		newBrain: func(*config.Config) (brain.Provider, error) { return &sessionOnlyTestBrain{}, nil },
		newTTS: func(*config.Config) (*voiceagent.TTSSet, error) {
			return &voiceagent.TTSSet{Primary: &fakeTTS{sampleRate: 24000, samples: 64}}, nil
		},
	}
	jobs := job.NewTable(t.Context())
	t.Cleanup(jobs.Close)
	pipeline.NewAgency(s.pipeline, jobs, nil, cfg, nil).Wire()
	if err := s.sessions.bindWork(s.pipeline, cfg, s.sessions.sess); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.sessions.closeWork(s.pipeline) })
	defer func() { _ = s.pipeline.CloseBrains() }()
	if err := s.initialize(); err != nil {
		t.Fatal(err)
	}

	previous := session.New("claude", "")
	turns := []brain.Turn{
		{Role: "user", Content: "The shared code is cobalt."},
		{Role: "assistant", Content: "Remembered.", PersonaID: "mira", PersonaDisplayName: "mira", ResponseID: "one"},
		{Role: "user", Content: "And the door is teal."},
		{Role: "assistant", Content: "Noted.", PersonaID: "mira", PersonaDisplayName: "mira", ResponseID: "two"},
	}
	if err := session.DefaultStore().SaveSelection(previous, turns, "mira", "mira", "saved-hash"); err != nil {
		t.Fatal(err)
	}
	if err := s.resume(t.Context(), previous.ID); err != nil {
		t.Fatal(err)
	}
	broken := session.New("claude", "")
	broken.Work = &work.Snapshot{Scope: work.WorkScope{SessionID: "another-session", RuntimeEpoch: "runtime", ConversationEpoch: "conversation"}}
	if err := session.DefaultStore().SaveSelection(broken, nil, "mira", "mira", "saved-hash"); err != nil {
		t.Fatal(err)
	}
	if err := s.resume(t.Context(), broken.ID); err == nil {
		t.Fatal("resuming a session with unusable work state succeeded")
	}
	if s.sessions.id() != previous.ID {
		t.Fatalf("live session = %s, want the previous one back", s.sessions.id())
	}
	if _, err := s.selector.Route(t.Context(), "Where were we?", false); err != nil {
		t.Fatal(err)
	}
	if err := s.sessions.save(s.sessions.history(s.pipeline)); err != nil {
		t.Fatal(err)
	}
	disk, err := session.Load(previous.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(disk.Turns) != len(turns) || disk.Turns[3].ResponseID != "two" {
		t.Fatalf("previous conversation on disk = %+v", disk.Turns)
	}
}

func TestServeFailedResumeKeepsAPersonaOnClaudeWithinItsShare(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	writeAckTestPersona(t, "mira", "You are Mira.")
	cfg := &config.Config{ActivePersona: "mira", BrainProvider: "claude", TTSProvider: "kokoro", VoiceWorkEnabled: true}
	initial, err := brain.NewProvider(cfg)
	if err != nil {
		t.Fatal(err)
	}
	manager := &voiceagent.LiveTTSManager{}
	t.Cleanup(manager.Close)
	s := &servePersonaSwitcher{cfg: cfg, pipeline: &pipeline.Pipeline{Brain: initial}, tts: manager,
		sessions: &sessionRef{sess: session.New("claude", "")},
		newBrain: brain.NewProvider,
		newTTS: func(*config.Config) (*voiceagent.TTSSet, error) {
			return &voiceagent.TTSSet{Primary: &fakeTTS{sampleRate: 24000, samples: 64}}, nil
		},
	}
	jobs := job.NewTable(t.Context())
	t.Cleanup(jobs.Close)
	pipeline.NewAgency(s.pipeline, jobs, nil, cfg, nil).Wire()
	if err := s.sessions.bindWork(s.pipeline, cfg, s.sessions.sess); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.sessions.closeWork(s.pipeline) })
	defer func() { _ = s.pipeline.CloseBrains() }()
	if err := s.initialize(); err != nil {
		t.Fatal(err)
	}

	previous := session.New("claude", "")
	turns := []brain.Turn{
		{Role: "user", Content: "Private: the safe code is 4711."},
		{Role: "assistant", Content: "Noted.", PersonaID: "ember", PersonaDisplayName: "Ember", ResponseID: "one"},
		{Role: "user", Content: "Mira, hello."},
		{Role: "assistant", Content: "Hello.", PersonaID: "mira", PersonaDisplayName: "mira", ResponseID: "two"},
	}
	if err := session.DefaultStore().SaveSelectionState(previous, turns, events.PersonaSelection{PersonaID: "mira", DisplayName: "mira", PromptHash: "saved-hash"}, brain.PersonaContexts{"mira": brain.SessionContext(turns, 2, 4)}); err != nil {
		t.Fatal(err)
	}
	if err := s.resume(t.Context(), previous.ID); err != nil {
		t.Fatal(err)
	}
	requireShare := func(stage string) {
		t.Helper()
		var held []string
		for _, turn := range s.pipeline.Brain.History() {
			held = append(held, turn.Content)
		}
		if strings.Contains(strings.Join(held, "\n"), "4711") || !slices.Contains(held, "Hello.") {
			t.Fatalf("%s: the provider holds %q", stage, held)
		}
	}
	requireShare("after resume")
	broken := session.New("claude", "")
	broken.Work = &work.Snapshot{Scope: work.WorkScope{SessionID: "another-session", RuntimeEpoch: "runtime", ConversationEpoch: "conversation"}}
	if err := session.DefaultStore().SaveSelection(broken, nil, "mira", "mira", "saved-hash"); err != nil {
		t.Fatal(err)
	}
	if err := s.resume(t.Context(), broken.ID); err == nil {
		t.Fatal("resuming a session with unusable work state succeeded")
	}
	requireShare("after a failed resume")
}
