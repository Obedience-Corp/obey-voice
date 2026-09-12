package personaruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/prompts"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
	"gopkg.in/yaml.v3"
)

type selectorTestBrain struct {
	mu         sync.Mutex
	history    []brain.Turn
	imports    [][]brain.Turn
	promptHash string
	thinkCalls int
	closed     chan struct{}
	closeOnce  sync.Once
	importErr  error
}

func newSelectorTestBrain(promptHash string, turns []brain.Turn) *selectorTestBrain {
	return &selectorTestBrain{promptHash: promptHash, history: cloneTestTurns(turns), closed: make(chan struct{})}
}

func (b *selectorTestBrain) ThinkStream(context.Context, string, brain.StreamOptions) (*brain.Stream, error) {
	b.mu.Lock()
	b.thinkCalls++
	b.mu.Unlock()
	chunks := make(chan string)
	done := make(chan brain.StreamResult, 1)
	close(chunks)
	done <- brain.StreamResult{}
	close(done)
	return &brain.Stream{Chunks: chunks, Done: done}, nil
}

func (b *selectorTestBrain) ThinkFull(context.Context, string, brain.StreamOptions) (string, error) {
	b.mu.Lock()
	b.thinkCalls++
	b.mu.Unlock()
	return "", nil
}

func (b *selectorTestBrain) ClearHistory() {}

func (b *selectorTestBrain) History() []brain.Turn {
	b.mu.Lock()
	defer b.mu.Unlock()
	return cloneTestTurns(b.history)
}

func (b *selectorTestBrain) LoadHistory(turns []brain.Turn) {
	_ = b.ImportHistory(turns)
}

func (b *selectorTestBrain) ImportHistory(turns []brain.Turn) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.importErr != nil {
		return b.importErr
	}
	b.history = cloneTestTurns(turns)
	b.imports = append(b.imports, cloneTestTurns(turns))
	return nil
}

func (b *selectorTestBrain) FreezePersonaPrompt() string { return b.promptHash }

func (b *selectorTestBrain) Close() error {
	b.closeOnce.Do(func() { close(b.closed) })
	return nil
}

func (b *selectorTestBrain) ThinkCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.thinkCalls
}

func (b *selectorTestBrain) IsClosed() bool {
	select {
	case <-b.closed:
		return true
	default:
		return false
	}
}

func cloneTestTurns(turns []brain.Turn) []brain.Turn {
	if len(turns) == 0 {
		return nil
	}
	out := make([]brain.Turn, len(turns))
	copy(out, turns)
	return out
}

type selectorTestFixture struct {
	selector  *Selector
	pipeline  *pipeline.Pipeline
	old       *selectorTestBrain
	catalog   persona.Catalog
	base      *config.Config
	factoryMu sync.Mutex
	factories []*selectorTestBrain
	store     *session.Store
	session   *session.Session
}

func newSelectorTestFixture(t *testing.T, profiles ...persona.Profile) *selectorTestFixture {
	t.Helper()
	if len(profiles) == 0 {
		profiles = []persona.Profile{testProfile("samantha", "Samantha", "samantha")}
	}
	root := t.TempDir()
	catalog := persona.Catalog{Dir: filepath.Join(root, "personas")}
	promptDir := filepath.Join(root, "prompts")
	for _, profile := range profiles {
		writeTestProfile(t, catalog.Dir, profile)
		writeTestPrompt(t, promptDir, profile.Prompts.Persona, "identity for "+profile.ID)
	}
	base := &config.Config{
		ActivePersona: profiles[0].ID,
		AgentName:     profiles[0].DisplayName,
		Persona:       profiles[0].Prompts.Persona,
		BrainProvider: "ollama",
		OllamaModel:   "test-model",
		TTSProvider:   "kokoro",
		TTSVoice:      "base-voice",
		PromptsDir:    promptDir,
	}
	initial, err := catalog.ResolveBinding(base, profiles[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	old := newSelectorTestBrain("initial-prompt", []brain.Turn{{Role: "user", Content: "before selection"}})
	p := &pipeline.Pipeline{Brain: old}
	store := session.NewStore(filepath.Join(root, "sessions"))
	sess := session.New("ollama", "test-model")
	f := &selectorTestFixture{pipeline: p, old: old, catalog: catalog, base: base, store: store, session: sess}
	factory := func(_ context.Context, cfg *config.Config) (brain.Provider, error) {
		hash, err := brain.PersonaPromptHash(cfg)
		if err != nil {
			return nil, err
		}
		candidate := newSelectorTestBrain(hash, nil)
		f.factoryMu.Lock()
		f.factories = append(f.factories, candidate)
		f.factoryMu.Unlock()
		return candidate, nil
	}
	persist := func(turns []brain.Turn, selected events.PersonaSelection) error {
		return store.SaveSelection(sess, turns, selected.PersonaID, selected.DisplayName, selected.PromptHash)
	}
	selector, err := NewSelector(SelectionOptions{
		Base: base, Catalog: catalog, Pipeline: p, SessionID: sess.ID, Initial: initial,
		Persist: persist, NewBrain: factory,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.selector = selector
	return f
}

func (f *selectorTestFixture) close(t *testing.T) {
	t.Helper()
	if err := f.pipeline.CloseBrains(); err != nil {
		t.Fatal(err)
	}
}

func testProfile(id, display, prompt string) persona.Profile {
	return persona.Profile{Schema: persona.Schema, ID: id, DisplayName: display, Brain: persona.Brain{Provider: "ollama", Model: id + "-model"}, TTS: persona.TTS{Provider: "kokoro", Voice: id + "-voice"}, Prompts: persona.PromptRefs{Persona: prompt}}
}

func writeTestProfile(t *testing.T, root string, profile persona.Profile) {
	t.Helper()
	dir := filepath.Join(root, profile.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := yaml.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "persona.yaml"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeTestPrompt(t *testing.T, root, name, identity string) {
	t.Helper()
	dir := filepath.Join(root, string(prompts.KindPersona))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	doc := prompts.Document{Schema: prompts.Schema, Prompt: prompts.Prompt{Name: name, Kind: prompts.KindPersona, SystemPrompt: prompts.SystemPrompt{Identity: identity}}}
	data, err := yaml.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".yaml"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSelectorKeepsCanonicalHistoryAcrossPersonaRoundTrip(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("samantha", "Samantha", "samantha"), testProfile("santa", "Santa", "santa"))
	defer f.close(t)

	if _, err := f.selector.Select(context.Background(), "santa", "name"); err != nil {
		t.Fatal(err)
	}
	factorySanta := f.factories[len(f.factories)-1]
	factorySanta.mu.Lock()
	factorySanta.history = append(factorySanta.history, brain.Turn{Role: "assistant", Content: "Santa reply"})
	factorySanta.mu.Unlock()
	if _, err := f.selector.Select(context.Background(), "samantha", "name"); err != nil {
		t.Fatal(err)
	}
	factoryRoundTrip := f.factories[len(f.factories)-1]
	want := []brain.Turn{{Role: "user", Content: "before selection"}, {Role: "assistant", Content: "Santa reply"}}
	if got := factoryRoundTrip.History(); !reflect.DeepEqual(got, want) {
		t.Fatalf("round-trip history = %#v, want %#v", got, want)
	}
	loaded, err := f.store.Load(f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SelectedPersonaID != "samantha" || !reflect.DeepEqual(loaded.Turns, want) {
		t.Fatalf("persisted selection/history = %q/%#v, want samantha/%#v", loaded.SelectedPersonaID, loaded.Turns, want)
	}
}

func TestSelectorRouteBareNameUsesLocalIdentityReply(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("samantha", "Samantha", "samantha"), testProfile("santa", "Santa", "santa"))
	defer f.close(t)

	admitted, err := f.selector.Route(context.Background(), "Santa.", false)
	if err != nil {
		t.Fatal(err)
	}
	if admitted.Request != "" || admitted.LocalReply != "Santa here." || admitted.Envelope.PersonaID != "santa" || admitted.Envelope.DisplayName != "Santa" {
		t.Fatalf("bare route admission = %+v", admitted)
	}
	if f.old.ThinkCount() != 0 {
		t.Fatalf("bare route invoked old model %d times", f.old.ThinkCount())
	}
	for _, candidate := range f.factories {
		if candidate.ThinkCount() != 0 {
			t.Fatalf("bare route invoked selected model %d times", candidate.ThinkCount())
		}
	}
	followup, err := f.selector.Route(context.Background(), "Can you hear me?", false)
	if err != nil {
		t.Fatal(err)
	}
	if followup.Envelope.PersonaID != "santa" || followup.Request != "Can you hear me?" {
		t.Fatalf("follow-up admission = %+v, want Santa and original request", followup)
	}
}

func TestSelectorAdmitRefreshesEditedPromptIdentity(t *testing.T) {
	b := testProfile("samantha", "Samantha", "samantha")
	f := newSelectorTestFixture(t, b)
	defer f.close(t)
	oldHash := f.selector.Snapshot().PromptHash
	writeTestPrompt(t, f.base.PromptsDir, "samantha", "edited identity")
	envelope, err := f.selector.Admit(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if envelope.PromptHash == oldHash || envelope.PersonaID != "samantha" {
		t.Fatalf("edited prompt envelope = %+v, old hash %q", envelope, oldHash)
	}
	if len(f.factories) != 1 {
		t.Fatalf("prompt edit created %d candidate brains, want 1", len(f.factories))
	}
}

func TestSelectorReportsMissingPromptAndUnsupportedProviderWithoutChangingState(t *testing.T) {
	missing := testProfile("missing", "Missing", "missing-prompt")
	f := newSelectorTestFixture(t, testProfile("samantha", "Samantha", "samantha"), missing)
	defer f.close(t)
	if err := os.Remove(filepath.Join(f.base.PromptsDir, "persona", "missing-prompt.yaml")); err != nil {
		t.Fatal(err)
	}
	before := f.selector.Snapshot()
	if _, err := f.selector.Select(context.Background(), "missing", "test"); err == nil {
		t.Fatal("Select accepted persona with missing prompt")
	}
	if got := f.selector.Snapshot(); !reflect.DeepEqual(got, before) || f.pipeline.Brain != f.old {
		t.Fatalf("missing-prompt failure changed state: snapshot=%+v brain_changed=%v", got, f.pipeline.Brain != f.old)
	}
	provider := testProfile("claude", "Claude", "claude")
	provider.Brain.Provider = "claude"
	writeTestProfile(t, f.catalog.Dir, provider)
	writeTestPrompt(t, f.base.PromptsDir, "claude", "claude identity")
	if _, err := f.selector.Select(context.Background(), "claude", "test"); err == nil {
		t.Fatal("Select accepted provider without verified projection")
	}
	if f.pipeline.Brain != f.old {
		t.Fatal("provider failure replaced the old brain")
	}
}

func TestSelectorPersistenceFailureKeepsOldSelectionAndClosesCandidate(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("samantha", "Samantha", "samantha"), testProfile("santa", "Santa", "santa"))
	defer f.close(t)
	wantErr := errors.New("disk full")
	f.selector.opts.Persist = func([]brain.Turn, events.PersonaSelection) error { return wantErr }
	before := f.selector.Snapshot()
	if _, err := f.selector.Select(context.Background(), "santa", "test"); !errors.Is(err, wantErr) {
		t.Fatalf("Select error = %v, want %v", err, wantErr)
	}
	if got := f.selector.Snapshot(); !reflect.DeepEqual(got, before) || f.pipeline.Brain != f.old {
		t.Fatalf("persistence failure changed state: snapshot=%+v brain_changed=%v", got, f.pipeline.Brain != f.old)
	}
	candidate := f.factories[len(f.factories)-1]
	if !candidate.IsClosed() {
		t.Fatal("candidate brain was not closed after persistence failure")
	}
}

func TestSelectorPublishesCommittedSelectionWithDurabilityWarning(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("samantha", "Samantha", "samantha"), testProfile("santa", "Santa", "santa"))
	defer f.close(t)
	bus := events.NewBus()
	f.pipeline.Events = bus
	warnings := 0
	bus.SubscribeAll(func(event events.Event) {
		if e, ok := event.(events.Error); ok && e.Stage == "session_durability" {
			warnings++
		}
	})
	persist := f.selector.opts.Persist
	f.selector.opts.Persist = func(turns []brain.Turn, selected events.PersonaSelection) error {
		if err := persist(turns, selected); err != nil {
			return err
		}
		return &session.WriteCommittedError{Err: errors.New("directory sync unavailable")}
	}
	selected, err := f.selector.Select(context.Background(), "santa", "ui")
	if err != nil {
		t.Fatal(err)
	}
	disk, err := f.store.Load(f.session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if selected.PersonaID != "santa" || disk.SelectedPersonaID != "santa" || f.pipeline.Brain == f.old || warnings != 1 {
		t.Fatalf("committed selection mismatch: selected=%+v disk=%+v warnings=%d", selected, disk, warnings)
	}
}

func TestSelectorCloseJoinsPublicationBeforeResourceCleanup(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("samantha", "Samantha", "samantha"), testProfile("santa", "Santa", "santa"))
	defer f.close(t)
	started, release := make(chan struct{}), make(chan struct{})
	persist := f.selector.opts.Persist
	f.selector.opts.Persist = func(turns []brain.Turn, selected events.PersonaSelection) error {
		close(started)
		<-release
		return persist(turns, selected)
	}
	selected := make(chan error, 1)
	go func() { _, err := f.selector.Select(context.Background(), "santa", "ui"); selected <- err }()
	<-started
	closed := make(chan struct{})
	go func() { f.selector.Close(); close(closed) }()
	select {
	case <-closed:
		t.Fatal("cleanup could close resources during selection persistence")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-selected; err != nil {
		t.Fatal(err)
	}
	<-closed
	if f.selector.Snapshot().PersonaID != "santa" {
		t.Fatal("committed selection not published before cleanup")
	}
	if _, err := f.selector.Admit(context.Background()); err == nil {
		t.Fatal("closed selector admitted another response")
	}
}

func TestSelectorCancellationClosesLateFactoryAndLaterSelectionSucceeds(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("samantha", "Samantha", "samantha"), testProfile("santa", "Santa", "santa"))
	defer f.close(t)
	started := make(chan struct{})
	release := make(chan struct{})
	late := newSelectorTestBrain("late", nil)
	var calls int
	f.selector.opts.NewBrain = func(ctx context.Context, cfg *config.Config) (brain.Provider, error) {
		calls++
		if calls == 1 {
			close(started)
			<-release
			return late, nil
		}
		hash, err := brain.PersonaPromptHash(cfg)
		if err != nil {
			return nil, err
		}
		return newSelectorTestBrain(hash, nil), nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := f.selector.Select(ctx, "santa", "canceled")
		result <- err
	}()
	<-started
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Select error = %v, want context canceled", err)
	}
	close(release)
	select {
	case <-late.closed:
	case <-time.After(time.Second):
		t.Fatal("late factory brain was not closed after cancellation")
	}
	if _, err := f.selector.Select(context.Background(), "santa", "retry"); err != nil {
		t.Fatalf("later selection failed: %v", err)
	}
}

func TestSelectorRejectsPreparationAfterConversationEpochChanges(t *testing.T) {
	f := newSelectorTestFixture(t, testProfile("samantha", "Samantha", "samantha"), testProfile("santa", "Santa", "santa"))
	defer f.close(t)
	if err := f.pipeline.BeginConversationIdentity(f.session.ID); err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	f.selector.opts.NewBrain = func(_ context.Context, cfg *config.Config) (brain.Provider, error) {
		close(started)
		<-release
		hash, err := brain.PersonaPromptHash(cfg)
		if err != nil {
			return nil, err
		}
		return newSelectorTestBrain(hash, nil), nil
	}
	result := make(chan error, 1)
	go func() {
		_, err := f.selector.Select(context.Background(), "santa", "ui")
		result <- err
	}()
	<-started
	if err := f.pipeline.BeginConversationIdentity(f.session.ID); err != nil {
		t.Fatal(err)
	}
	close(release)
	err := <-result
	if err == nil || !strings.Contains(err.Error(), "retired conversation boundary") {
		t.Fatalf("selection after epoch change = %v, want boundary error", err)
	}
	if got := f.selector.Snapshot().PersonaID; got != "samantha" {
		t.Fatalf("stale selection changed responder to %q", got)
	}
}
