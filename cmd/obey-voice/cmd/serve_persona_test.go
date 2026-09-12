//go:build !integration

package cmd

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/Obedience-Corp/obey-voice/internal/netapi"
	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
)

type servePersonaTestBrain struct {
	history []brain.Turn
	closed  int
	hash    string
}

func TestServePersonaResumeUsesSavedSelectionAndSession(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	writeAckTestPersona(t, "santa", "You are Santa.")
	cfg := &config.Config{ActivePersona: "samantha", BrainProvider: "ollama", TTSProvider: "kokoro"}
	s := newSwitcherForTest(t, cfg)
	defer func() { _ = s.pipeline.CloseBrains() }()
	if err := s.initialize(); err != nil {
		t.Fatal(err)
	}
	oldID := s.sessions.id()
	restored := session.New("ollama", "test")
	turns := []brain.Turn{{Role: "user", Content: "The shared code is cobalt."}, {Role: "assistant", Content: "Remembered.", PersonaID: "santa", PersonaDisplayName: "Santa"}}
	if err := session.DefaultStore().SaveSelection(restored, turns, "santa", "Santa", "saved-hash"); err != nil {
		t.Fatal(err)
	}
	if err := s.resume(context.Background(), restored.ID); err != nil {
		t.Fatal(err)
	}
	selected := s.selector.Snapshot()
	if selected.SessionID != restored.ID || selected.PersonaID != "santa" || s.sessions.id() != restored.ID || selected.SessionID == oldID {
		t.Fatalf("restored selection = %+v, ref = %s", selected, s.sessions.id())
	}
	if got := s.pipeline.Brain.History(); !reflect.DeepEqual(got, turns) {
		t.Fatalf("restored history = %+v", got)
	}
	admitted, err := s.selector.Route(context.Background(), "What was the code?", false)
	if err != nil {
		t.Fatal(err)
	}
	if admitted.Envelope.SessionID != restored.ID || admitted.Envelope.PersonaID != "santa" {
		t.Fatalf("resumed response = %+v", admitted)
	}
}

func TestServePersonaResumeDeletedProfileRequiresExplicitSelection(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	cfg := &config.Config{ActivePersona: "samantha", BrainProvider: "ollama", TTSProvider: "kokoro"}
	s := newSwitcherForTest(t, cfg)
	defer func() { _ = s.pipeline.CloseBrains() }()
	if err := s.initialize(); err != nil {
		t.Fatal(err)
	}
	restored := session.New("ollama", "test")
	turns := []brain.Turn{{Role: "assistant", Content: "Historical Santa reply", PersonaID: "deleted", PersonaDisplayName: "Santa"}}
	if err := session.DefaultStore().SaveSelection(restored, turns, "deleted", "Santa", "original-hash"); err != nil {
		t.Fatal(err)
	}
	if err := s.resume(context.Background(), restored.ID); err != nil {
		t.Fatal(err)
	}
	if got := s.pipeline.Brain.History(); !reflect.DeepEqual(got, turns) {
		t.Fatalf("deleted persona history = %+v", got)
	}
	if _, err := s.selector.Route(context.Background(), "Hello again", false); err == nil {
		t.Fatal("missing restored persona silently used previous responder")
	}
	disk, err := session.Load(restored.ID)
	if err != nil {
		t.Fatal(err)
	}
	if disk.SelectedPersonaID != "deleted" {
		t.Fatalf("failed restore rewrote saved identity: %+v", disk)
	}
	if _, err := s.apply("samantha"); err != nil {
		t.Fatal(err)
	}
	admitted, err := s.selector.Route(context.Background(), "Hello again", false)
	if err != nil {
		t.Fatal(err)
	}
	if admitted.Envelope.SessionID != restored.ID || admitted.Envelope.PersonaID != "samantha" {
		t.Fatalf("explicit recovery = %+v", admitted)
	}
	if got := s.pipeline.Brain.History(); !reflect.DeepEqual(got, turns) {
		t.Fatalf("recovery rewrote historical identity: %+v", got)
	}
}

func (*servePersonaTestBrain) ThinkStream(context.Context, string, brain.StreamOptions) (*brain.Stream, error) {
	return nil, nil
}
func (*servePersonaTestBrain) ThinkFull(context.Context, string, brain.StreamOptions) (string, error) {
	return "", nil
}
func (*servePersonaTestBrain) ClearHistory()  {}
func (b *servePersonaTestBrain) Close() error { b.closed++; return nil }
func (b *servePersonaTestBrain) History() []brain.Turn {
	return append([]brain.Turn(nil), b.history...)
}
func (b *servePersonaTestBrain) FreezePersonaPrompt() string { return b.hash }
func (b *servePersonaTestBrain) ImportHistory(turns []brain.Turn) error {
	b.history = append([]brain.Turn(nil), turns...)
	return nil
}
func serveBrainForConfig(cfg *config.Config) (brain.Provider, error) {
	hash, err := brain.PersonaPromptHash(cfg)
	if err != nil {
		return nil, err
	}
	return &servePersonaTestBrain{hash: hash}, nil
}

func (b *servePersonaTestBrain) LoadHistory(turns []brain.Turn) {
	b.history = append([]brain.Turn(nil), turns...)
}

func TestServePersonaSwitcherPreservesSessionAndHistory(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	profile := &persona.Profile{
		Schema:      persona.Schema,
		ID:          "pirate",
		DisplayName: "Captain",
		Brain:       persona.Brain{Provider: "ollama", Model: "pirate-model"},
		TTS:         persona.TTS{Provider: "kokoro", Voice: "pm-pirate"},
		Prompts:     persona.PromptRefs{Persona: "pirate"},
	}
	if err := persona.Write(profile, false); err != nil {
		t.Fatal(err)
	}
	// The ack hashes the identity text, so the persona needs a real document.
	if err := persona.WriteSystemPrompt("pirate", "You are Captain, a pirate."); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		ActivePersona: "samantha",
		AgentName:     "Samantha",
		Persona:       "samantha",
		BrainProvider: "ollama",
		OllamaModel:   "old-model",
		TTSProvider:   "kokoro",
		TTSVoice:      "old-voice",
	}
	writeAckTestPersona(t, "samantha", "You are Samantha.")
	oldHash, err := brain.PersonaPromptHash(cfg)
	if err != nil {
		t.Fatal(err)
	}
	oldBrain := &servePersonaTestBrain{hash: oldHash, history: []brain.Turn{{Role: "user", Content: "old turn"}}}
	newBrain := &servePersonaTestBrain{}
	p := &pipeline.Pipeline{Brain: oldBrain}
	manager := &voiceagent.LiveTTSManager{}
	t.Cleanup(manager.Close)
	ref := &sessionRef{sess: session.New(cfg.BrainProvider, cfg.OllamaModel)}
	oldSessionID := ref.sess.ID

	var brainCfg, ttsCfg config.Config
	switcher := &servePersonaSwitcher{
		cfg: cfg, pipeline: p, tts: manager, sessions: ref,
		newBrain: func(got *config.Config) (brain.Provider, error) {
			brainCfg = *got
			newBrain.hash, _ = brain.PersonaPromptHash(got)
			return newBrain, nil
		},
		newTTS: func(got *config.Config) (*voiceagent.TTSSet, error) {
			ttsCfg = *got
			return &voiceagent.TTSSet{Primary: &fakeTTS{sampleRate: 24000, samples: 64}}, nil
		},
	}

	ack, err := switcher.apply("pirate")
	if err != nil {
		t.Fatalf("apply() error = %v", err)
	}
	wantHash, err := persona.PromptHashFor("pirate")
	if err != nil {
		t.Fatal(err)
	}
	if ack != (netapi.PersonaAck{ID: "pirate", DisplayName: "Captain", PromptHash: wantHash}) {
		t.Fatalf("ack = %+v, want prompt_hash %q", ack, wantHash)
	}
	if p.Brain != newBrain {
		t.Fatal("pipeline brain was not replaced")
	}
	if err := p.CloseBrains(); err != nil {
		t.Fatal(err)
	}
	if oldBrain.closed != 1 || newBrain.closed != 1 {
		t.Fatalf("brain close counts old=%d new=%d", oldBrain.closed, newBrain.closed)
	}
	if brainCfg.OllamaModel != "pirate-model" || brainCfg.Persona != "pirate" {
		t.Fatalf("brain config = model %q persona %q", brainCfg.OllamaModel, brainCfg.Persona)
	}
	if ttsCfg.TTSProvider != "kokoro" || ttsCfg.TTSVoice != "pm-pirate" {
		t.Fatalf("TTS config = provider %q voice %q", ttsCfg.TTSProvider, ttsCfg.TTSVoice)
	}
	if cfg.ActivePersona != "samantha" || cfg.AgentName != "Samantha" {
		t.Fatalf("live config = persona %q agent %q", cfg.ActivePersona, cfg.AgentName)
	}
	if ref.sess.ID != oldSessionID {
		t.Fatal("persona switch replaced the conversation")
	}
	if got := p.Brain.History(); len(got) != 1 || got[0].Content != "old turn" {
		t.Fatalf("shared history lost: %+v", got)
	}
	if ref.sess.SelectedPersonaID != "pirate" || ref.sess.Model != "old-model" {
		t.Fatalf("new session = provider %q model %q", ref.sess.Provider, ref.sess.Model)
	}
}

// newSwitcherForTest builds a switcher whose brain and TTS construction are
// stubs, so a test exercises the persona resolution and ack, not the runtime.
func newSwitcherForTest(t *testing.T, cfg *config.Config) *servePersonaSwitcher {
	t.Helper()
	if _, err := persona.Load(persona.ActiveID(cfg)); err != nil {
		writeAckTestPersona(t, persona.ActiveID(cfg), "You are the initial responder.")
	}
	binding, err := persona.ResolveBinding(cfg, persona.ActiveID(cfg))
	if err != nil {
		t.Fatal(err)
	}
	current, err := serveBrainForConfig(binding.Config())
	if err != nil {
		t.Fatal(err)
	}
	manager := &voiceagent.LiveTTSManager{}
	t.Cleanup(manager.Close)
	return &servePersonaSwitcher{cfg: cfg, pipeline: &pipeline.Pipeline{Brain: current}, tts: manager,
		sessions: &sessionRef{sess: session.New(cfg.BrainProvider, cfg.OllamaModel)},
		newBrain: serveBrainForConfig,
		newTTS: func(*config.Config) (*voiceagent.TTSSet, error) {
			return &voiceagent.TTSSet{Primary: &fakeTTS{sampleRate: 24000, samples: 64}}, nil
		},
	}
}

func writeAckTestPersona(t *testing.T, id, body string) {
	t.Helper()
	if err := persona.Write(&persona.Profile{
		Schema: persona.Schema, ID: id, DisplayName: id,
		TTS: persona.TTS{Provider: "kokoro", Voice: "af_heart"}, Prompts: persona.PromptRefs{Persona: id},
	}, false); err != nil {
		t.Fatal(err)
	}
	if body == "" {
		return
	}
	if err := persona.WriteSystemPrompt(id, body); err != nil {
		t.Fatal(err)
	}
}

// The field exists so a client can tell whether the model sees the prompt it
// just edited. A document name would never change and answer nothing.
func TestServePersonaAckHashTracksTheDocument(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	writeAckTestPersona(t, "scribe", "You are Scribe, a careful note taker.")
	cfg := &config.Config{ActivePersona: "scribe", BrainProvider: "ollama", TTSProvider: "kokoro"}

	ack, err := newSwitcherForTest(t, cfg).apply("scribe")
	if err != nil {
		t.Fatalf("apply() error = %v", err)
	}
	if ack.PromptHash == "scribe" {
		t.Fatal("prompt_hash is the document name, not a hash")
	}
	if len(ack.PromptHash) != persona.PromptHashPrefix {
		t.Fatalf("prompt_hash = %q, want %d hex chars", ack.PromptHash, persona.PromptHashPrefix)
	}
	doc, err := persona.DescribePrompt(mustLoadPersona(t, "scribe"))
	if err != nil {
		t.Fatal(err)
	}
	if ack.PromptHash != doc.Hash {
		t.Fatalf("prompt_hash = %q, want the assembled document hash %q", ack.PromptHash, doc.Hash)
	}

	// Edit the body: the same persona must now ack a different hash.
	if err := persona.WriteSystemPrompt("scribe", "You are Scribe, and you answer in one line."); err != nil {
		t.Fatal(err)
	}
	edited, err := newSwitcherForTest(t, cfg).apply("scribe")
	if err != nil {
		t.Fatalf("apply() after edit error = %v", err)
	}
	if edited.PromptHash == ack.PromptHash {
		t.Fatalf("prompt_hash stayed %q after the document changed", edited.PromptHash)
	}
}

func TestServePersonaMissingPromptKeepsPreviousSelection(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	writeAckTestPersona(t, "ghostwriter", "")
	cfg := &config.Config{ActivePersona: "obey", BrainProvider: "ollama", TTSProvider: "kokoro"}
	switcher := newSwitcherForTest(t, cfg)
	previous := switcher.pipeline.Brain
	if _, err := switcher.apply("ghostwriter"); err == nil {
		t.Fatal("missing prompt accepted")
	}
	if switcher.pipeline.Brain != previous || switcher.sessions.sess.SelectedPersonaID != "obey" {
		t.Fatal("failed preparation changed selection")
	}
}

func mustLoadPersona(t *testing.T, id string) *persona.Profile {
	t.Helper()
	p, err := persona.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestServePersonaFailureClosesUnusedBrain(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	writeAckTestPersona(t, "worker", "You are a helpful voice.")
	cfg := &config.Config{BrainProvider: "ollama", TTSProvider: "kokoro"}
	switcher := newSwitcherForTest(t, cfg)
	next := &servePersonaTestBrain{}
	switcher.newBrain = func(cfg *config.Config) (brain.Provider, error) {
		next.hash, _ = brain.PersonaPromptHash(cfg)
		return next, nil
	}
	switcher.newTTS = func(*config.Config) (*voiceagent.TTSSet, error) { return nil, fmt.Errorf("TTS construction failed") }
	if _, err := switcher.apply("worker"); err == nil {
		t.Fatal("failed TTS accepted")
	}
	if next.closed != 1 {
		t.Fatalf("unused brain closed %d times", next.closed)
	}
	if switcher.pipeline.Brain.(*servePersonaTestBrain).closed != 0 {
		t.Fatal("old brain closed during failed replacement")
	}
}
