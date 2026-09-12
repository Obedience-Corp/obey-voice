//go:build !integration

package cmd

import (
	"context"
	"reflect"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
)

type canonicalSnapshotBrain struct {
	*servePersonaTestBrain
	canonical []brain.Turn
}

func (b *canonicalSnapshotBrain) CanonicalHistory() []brain.Turn { return b.canonical }

func TestServeSnapshotUsesCanonicalHistoryAndRejectsMixedSession(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	cfg := &config.Config{ActivePersona: "samantha", BrainProvider: "ollama", TTSProvider: "kokoro", VoiceWorkEnabled: true}
	s := newSwitcherForTest(t, cfg)
	jobs := job.NewTable(t.Context())
	t.Cleanup(jobs.Close)
	pipeline.NewAgency(s.pipeline, jobs, nil, cfg, nil).Wire()
	checkpoint, err := bindWorkSession(s.pipeline, cfg, s.sessions.sess, session.DefaultStore())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := closeWorkSession(s.pipeline, checkpoint); err != nil {
			t.Error(err)
		}
	})
	defer func() { _ = s.pipeline.CloseBrains() }()
	if err := s.initialize(); err != nil {
		t.Fatal(err)
	}
	raw := []brain.Turn{{Role: "assistant", Content: "Remember cobalt", PersonaID: "santa", PersonaDisplayName: "Santa", ResponseID: "response-old"}}
	old := s.pipeline.Brain.(*servePersonaTestBrain)
	old.history = []brain.Turn{{Role: "assistant", Content: "Bounded summary without original identity"}}
	s.pipeline.Brain = &canonicalSnapshotBrain{servePersonaTestBrain: old, canonical: raw}
	got, err := s.snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Transcript, raw) || got.Selection.PersonaID != "samantha" {
		t.Fatalf("snapshot = %+v", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.snapshot(ctx); err == nil {
		t.Fatal("canceled snapshot succeeded")
	}
	s.sessions.mu.Lock()
	s.sessions.sess.ID = "different-session"
	s.sessions.mu.Unlock()
	if _, err := s.snapshot(context.Background()); err == nil {
		t.Fatal("snapshot combined different session owners")
	}
}

func TestServeClearKeepsSelectionInNewWorkEpoch(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	cfg := &config.Config{ActivePersona: "samantha", BrainProvider: "ollama", TTSProvider: "kokoro", VoiceWorkEnabled: true}
	s := newSwitcherForTest(t, cfg)
	jobs := job.NewTable(t.Context())
	t.Cleanup(jobs.Close)
	pipeline.NewAgency(s.pipeline, jobs, nil, cfg, nil).Wire()
	checkpoint, err := bindWorkSession(s.pipeline, cfg, s.sessions.sess, session.DefaultStore())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := closeWorkSession(s.pipeline, checkpoint); err != nil {
			t.Error(err)
		}
	})
	defer func() { _ = s.pipeline.CloseBrains() }()
	if err := s.initialize(); err != nil {
		t.Fatal(err)
	}
	before := s.selector.Snapshot()
	if err := s.clear(context.Background()); err != nil {
		t.Fatal(err)
	}
	after := s.selector.Snapshot()
	scope := s.pipeline.WorkSnapshot().Scope
	if after.PersonaID != before.PersonaID || after.SessionID != before.SessionID || after.ConversationEpoch == before.ConversationEpoch || after.ConversationEpoch != scope.ConversationEpoch {
		t.Fatalf("clear before=%+v after=%+v scope=%+v", before, after, scope)
	}
	if _, err := s.selector.Route(context.Background(), "continue", false); err != nil {
		t.Fatalf("clear disabled unchanged responder: %v", err)
	}
}

func TestServeSnapshotCapabilityWorksWithoutConversationWork(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	cfg := &config.Config{ActivePersona: "samantha", BrainProvider: "ollama", TTSProvider: "kokoro", VoiceWorkEnabled: false}
	s := newSwitcherForTest(t, cfg)
	jobs := job.NewTable(t.Context())
	t.Cleanup(jobs.Close)
	pipeline.NewAgency(s.pipeline, jobs, nil, cfg, nil).Wire()
	checkpoint, err := bindWorkSession(s.pipeline, cfg, s.sessions.sess, session.DefaultStore())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := closeWorkSession(s.pipeline, checkpoint); err != nil {
			t.Error(err)
		}
	})
	defer func() { _ = s.pipeline.CloseBrains() }()
	if err := s.initialize(); err != nil {
		t.Fatal(err)
	}
	if s.pipeline.WorkCoordinator() != nil {
		t.Fatal("work-disabled bind created a coordinator")
	}
	raw := []brain.Turn{{Role: "assistant", Content: "Remember cobalt", PersonaID: "santa", PersonaDisplayName: "Santa", ResponseID: "response-old"}}
	s.pipeline.Brain = &canonicalSnapshotBrain{servePersonaTestBrain: s.pipeline.Brain.(*servePersonaTestBrain), canonical: raw}
	provider := s.snapshotProvider()
	if provider == nil {
		t.Fatal("work-disabled host did not advertise its host-owned snapshot")
	}
	got, err := provider(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionID != s.sessions.id() || got.ConversationEpoch == "" || !reflect.DeepEqual(got.Transcript, raw) {
		t.Fatalf("snapshot = %+v", got)
	}
	if got.ConversationEpoch != s.pipeline.ConversationScope().ConversationEpoch {
		t.Fatalf("snapshot epoch %q != pipeline epoch %q", got.ConversationEpoch, s.pipeline.ConversationScope().ConversationEpoch)
	}
	if err := s.pipeline.CloseWork(); err != nil {
		t.Fatal(err)
	}
	if !s.pipeline.ConversationRetired() || s.snapshotProvider() != nil {
		t.Fatalf("closed normal conversation retained snapshot capability: retired=%t provider=%v", s.pipeline.ConversationRetired(), s.snapshotProvider() != nil)
	}
}

func TestServeClearRotatesNormalConversationEpoch(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	cfg := &config.Config{ActivePersona: "samantha", BrainProvider: "ollama", TTSProvider: "kokoro", VoiceWorkEnabled: false}
	s := newSwitcherForTest(t, cfg)
	jobs := job.NewTable(t.Context())
	t.Cleanup(jobs.Close)
	pipeline.NewAgency(s.pipeline, jobs, nil, cfg, nil).Wire()
	checkpoint, err := bindWorkSession(s.pipeline, cfg, s.sessions.sess, session.DefaultStore())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := closeWorkSession(s.pipeline, checkpoint); err != nil {
			t.Error(err)
		}
	})
	defer func() { _ = s.pipeline.CloseBrains() }()
	if err := s.initialize(); err != nil {
		t.Fatal(err)
	}
	before := s.selector.Snapshot()
	if err := s.clear(context.Background()); err != nil {
		t.Fatal(err)
	}
	after := s.selector.Snapshot()
	if before.SessionID != after.SessionID || before.ConversationEpoch == after.ConversationEpoch || after.ConversationEpoch != s.pipeline.ConversationScope().ConversationEpoch {
		t.Fatalf("clear before=%+v after=%+v scope=%+v", before, after, s.pipeline.ConversationScope())
	}
	if s.pipeline.ConversationRetired() {
		t.Fatal("clear retired the normal conversation")
	}
}

func TestSessionRefResumeRebindsNormalConversationIdentity(t *testing.T) {
	config.SetConfigDirForTest(t, t.TempDir())
	cfg := &config.Config{ActivePersona: "samantha", BrainProvider: "ollama", TTSProvider: "kokoro", VoiceWorkEnabled: false}
	s := newSwitcherForTest(t, cfg)
	jobs := job.NewTable(t.Context())
	t.Cleanup(jobs.Close)
	pipeline.NewAgency(s.pipeline, jobs, nil, cfg, nil).Wire()
	ref := s.sessions
	if err := ref.bindWork(s.pipeline, cfg, ref.sess); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ref.closeWork(s.pipeline); _ = s.pipeline.CloseBrains() }()
	oldScope := s.pipeline.ConversationScope()
	next := session.New("ollama", "next-model")
	if err := next.Save([]brain.Turn{{Role: "user", Content: "restored conversation"}}); err != nil {
		t.Fatal(err)
	}
	if err := ref.resume(s.pipeline, cfg, next.ID); err != nil {
		t.Fatal(err)
	}
	newScope := s.pipeline.ConversationScope()
	if ref.id() != next.ID || newScope.SessionID != next.ID || newScope == oldScope || !newScope.Valid() || s.pipeline.WorkCoordinator() != nil {
		t.Fatalf("normal resume identity old=%+v new=%+v session=%s coordinator=%v", oldScope, newScope, ref.id(), s.pipeline.WorkCoordinator() != nil)
	}
	if got := s.pipeline.Brain.History(); len(got) != 1 || got[0].Content != "restored conversation" {
		t.Fatalf("restored history = %+v", got)
	}
}
