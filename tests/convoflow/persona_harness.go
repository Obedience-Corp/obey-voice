package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/viper"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	personaruntime "github.com/Obedience-Corp/obey-voice/internal/persona/runtime"
	"github.com/Obedience-Corp/obey-voice/internal/worksession"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

// loadFixtureConfig deliberately uses a fresh Viper instance. Persona proof
// runs must never mutate the process-wide config directory or read global
// settings as an accidental second input.
func loadFixtureConfig(path string) (*config.Config, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("fixture config path is required")
	}
	v := viper.New()
	// Match the owning config default: zero would erase provider history after
	// every turn even though the canonical transcript remains complete.
	if spec, ok := config.SpecFor("max_history"); ok {
		v.SetDefault(spec.Key, spec.Default)
	}
	// Omission means unity, as in production. Explicit zero must remain mute.
	if spec, ok := config.SpecFor("playback_volume"); ok {
		v.SetDefault(spec.Key, spec.Default)
	}
	v.SetConfigFile(path)
	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("read fixture config %s: %w", path, err)
	}
	var cfg config.Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("decode fixture config %s: %w", path, err)
	}
	return &cfg, nil
}

type personaTurnState struct {
	Selection       events.PersonaSelection `json:"selection"`
	Envelope        events.ResponseEnvelope `json:"envelope"`
	History         []brain.Turn            `json:"history"`
	BrainCalls      int                     `json:"brain_calls"`
	WorkerCalls     int                     `json:"worker_calls"`
	Work            work.Snapshot           `json:"work"`
	WorkAtSelection work.Snapshot           `json:"work_at_selection,omitempty"`
}

type personaRunner interface {
	selectPersona(context.Context, string) error
	personaState() personaTurnState
	writePersonaArtifacts(string) error
	validatePersonaRun() error
}

func (r *liveRunner) validatePersonaRun() error {
	if r.persona == nil {
		return nil
	}
	return r.persona.validate()
}

type personaRuntime struct {
	selector           *personaruntime.Selector
	store              *session.Store
	session            *session.Session
	checkpoint         *worksession.Checkpoint
	mu                 sync.Mutex
	selections         []events.PersonaSelection
	envelopes          []events.ResponseEnvelope
	responses          []map[string]any
	selectionTimings   []map[string]any
	callCount          func() int
	lastCalls          int
	invocationSnapshot func() any
	saveErr            error
	sessionDir         string
	recovery           []recoveryArtifact
	backupFn           func([]brain.Turn) error
}

func (r *personaRuntime) state(p brain.Provider, calls int) personaTurnState {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.callCount != nil {
		calls = r.callCount()
	}
	delta := calls - r.lastCalls
	if delta < 0 {
		delta = calls
	}
	r.lastCalls = calls
	st := personaTurnState{Selection: r.selector.Snapshot(), History: append([]brain.Turn(nil), p.History()...), BrainCalls: delta}
	if n := len(r.envelopes); n > 0 {
		st.Envelope = r.envelopes[n-1]
	}
	return st
}

func (r *personaRuntime) recordSelection(s events.PersonaSelection) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.selections = append(r.selections, s)
}
func (r *personaRuntime) recordSaveError(err error) {
	if err != nil {
		r.mu.Lock()
		if r.saveErr == nil {
			r.saveErr = err
		}
		r.mu.Unlock()
	}
}
func (r *personaRuntime) recordEnvelope(e events.ResponseEnvelope) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.envelopes = append(r.envelopes, e)
	model := ""
	if r.selector != nil {
		model = r.selector.Snapshot().BrainModel
	}
	r.responses = append(r.responses, map[string]any{"response_id": e.ResponseID, "persona_id": e.PersonaID, "display_name": e.DisplayName, "brain_model": model, "voice": e.Voice, "voice_provider": e.VoiceProvider})
}
func (r *personaRuntime) writeArtifacts(dir string, p brain.Provider, calls []map[string]any) error {
	r.mu.Lock()
	selections := append([]events.PersonaSelection(nil), r.selections...)
	envelopes := append([]events.ResponseEnvelope(nil), r.envelopes...)
	responses := append([]map[string]any(nil), r.responses...)
	selectionTimings := append([]map[string]any(nil), r.selectionTimings...)
	r.mu.Unlock()
	history := p.History()
	write := func(name string, v any) error {
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return err
		}
		b = append(b, '\n')
		return os.WriteFile(filepath.Join(dir, name), b, 0o644)
	}
	if err := write("canonical-history.json", history); err != nil {
		return err
	}
	if err := write("persona-selections.json", selections); err != nil {
		return err
	}
	if err := write("response-envelopes.json", envelopes); err != nil {
		return err
	}
	if err := write("response-records.json", responses); err != nil {
		return err
	}
	if err := write("selection-timings.json", selectionTimings); err != nil {
		return err
	}
	if r.checkpoint != nil {
		flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := r.checkpoint.Wait(flushCtx)
		cancel()
		if err != nil {
			return fmt.Errorf("flush work checkpoint: %w", err)
		}
	}
	if r.store != nil && r.session != nil {
		reloaded, err := r.store.Load(r.session.ID)
		if err != nil {
			return fmt.Errorf("reload session artifact: %w", err)
		}
		if err := write("session-reloaded.json", reloaded); err != nil {
			return err
		}
	}
	if r.invocationSnapshot != nil {
		if err := write("brain-invocations.json", r.invocationSnapshot()); err != nil {
			return err
		}
	} else if err := write("brain-invocations.json", calls); err != nil {
		return err
	}
	return r.writeRecoveryArtifacts(write)
}

func (r *personaRuntime) recordSelectionTiming(start time.Time, before, after events.PersonaSelection, source string, err error, workAtSelection work.Snapshot) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row := map[string]any{
		"source":                          source,
		"started_at":                      start.UTC().Format(time.RFC3339Nano),
		"ended_at":                        time.Now().UTC().Format(time.RFC3339Nano),
		"duration_ns":                     time.Since(start).Nanoseconds(),
		"prior_persona":                   before.PersonaID,
		"selected_persona":                after.PersonaID,
		"session_id":                      after.SessionID,
		"conversation_epoch":              after.ConversationEpoch,
		"work_revision":                   workAtSelection.Revision,
		"work_persisted":                  workAtSelection.PersistedRevision,
		"work_running_at_selection":       workHasRunningJob(workAtSelection),
		"work_completed_before_selection": !workHasRunningJob(workAtSelection) && workHasTerminalJob(workAtSelection),
	}
	if err != nil {
		row["error"] = err.Error()
	}
	r.selectionTimings = append(r.selectionTimings, row)
}

func (r *personaRuntime) validate() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.saveErr != nil {
		return fmt.Errorf("session persistence: %w", r.saveErr)
	}
	if len(r.selections) == 0 {
		return fmt.Errorf("persona proof recorded no selections")
	}
	sessionID, epoch := r.selections[0].SessionID, r.selections[0].ConversationEpoch
	for _, s := range r.selections[1:] {
		if s.SessionID != sessionID || s.ConversationEpoch != epoch {
			return fmt.Errorf("persona selection changed session scope: %q/%q then %q/%q", sessionID, epoch, s.SessionID, s.ConversationEpoch)
		}
	}
	for _, e := range r.envelopes {
		if e.SessionID != sessionID || e.ConversationEpoch != epoch {
			return fmt.Errorf("response envelope changed session scope: %q/%q", e.SessionID, e.ConversationEpoch)
		}
	}
	return nil
}

func configurePersona(r *liveRunner, base *config.Config, catalog persona.Catalog, initial *persona.SessionBinding, runDir string) error {
	return configurePersonaWithFactory(r, base, catalog, initial, runDir, func(cfg *config.Config) (brain.Provider, error) {
		b, err := brain.NewProvider(cfg)
		if err != nil {
			return nil, err
		}
		wrapped, obs := observeProvider(b)
		r.observations = append(r.observations, obs)
		return wrapped, nil
	})
}

func configurePersonaWithFactory(r *liveRunner, base *config.Config, catalog persona.Catalog, initial *persona.SessionBinding, runDir string, makeBrain func(*config.Config) (brain.Provider, error)) error {
	if base == nil || initial == nil {
		return fmt.Errorf("persona: base and initial binding are required")
	}
	sess := session.New(initial.BrainProvider, initial.BrainModel)
	store := session.NewStore(filepath.Join(runDir, "sessions"))
	if err := store.SaveSelection(sess, r.liveProvider.History(), initial.PersonaID, initial.DisplayName, ""); err != nil {
		return fmt.Errorf("save initial selection: %w", err)
	}
	rt := &personaRuntime{store: store, session: sess, sessionDir: filepath.Join(runDir, "sessions")}
	// The task02 selector fixture historically enabled the work authority even
	// when the operator config omitted the feature flag. Keep that authority for
	// persona proof while the production adapter retains its disabled no-op path.
	workCfg := initial.Config()
	if workCfg != nil && !workCfg.VoiceWorkEnabled {
		copy := *workCfg
		copy.VoiceWorkEnabled = true
		workCfg = &copy
	}
	checkpoint, err := worksession.Bind(r.agent.Pipeline, workCfg, sess, store)
	if err != nil {
		return err
	}
	rt.checkpoint = checkpoint
	selector, err := personaruntime.NewSelector(personaruntime.SelectionOptions{
		Base: base, Catalog: catalog, Pipeline: r.agent.Pipeline, Voices: r.voices,
		SessionID: sess.ID, Initial: initial,
		Persist: func(turns []brain.Turn, selected events.PersonaSelection) error {
			return store.SaveSelection(sess, turns, selected.PersonaID, selected.DisplayName, selected.PromptHash)
		},
		NewBrain: func(ctx context.Context, cfg *config.Config) (brain.Provider, error) { return makeBrain(cfg) },
		NewWorker: func(ctx context.Context, cfg *config.Config) (brain.Provider, error) {
			if r.workerFactory != nil {
				return r.workerFactory(cfg)
			}
			return brain.NewWorkerProvider(cfg)
		},
	})
	if err != nil {
		_ = r.agent.Pipeline.CloseWork()
		_ = checkpoint.Close()
		return err
	}
	rt.selector = selector
	rt.callCount = func() int {
		n := 0
		for _, obs := range r.observations {
			s, _ := obs.calls()
			n += s
		}
		return n
	}
	rt.invocationSnapshot = func() any {
		out := make([]map[string]any, 0, len(r.observations))
		for _, obs := range r.observations {
			s, _ := obs.calls()
			out = append(out, map[string]any{"calls": s})
		}
		return out
	}
	rt.recordSelection(selector.Snapshot())
	r.persona = rt
	r.agent.Pipeline.OnTurn = func() {
		rt.recordSaveError(store.Save(sess, r.agent.Pipeline.Brain.History()))
	}
	r.agent.AdmitInput = func(ctx context.Context, input string, background bool) (pipeline.ResponseAdmission, error) {
		before := selector.Snapshot()
		workBefore := r.workSnapshot()
		started := time.Now()
		ad, err := selector.Route(ctx, input, background)
		if err == nil {
			after := selector.Snapshot()
			if after.BindingHash != before.BindingHash {
				rt.recordSelection(after)
				rt.recordSelectionTiming(started, before, after, "route", nil, workBefore)
			}
			rt.recordEnvelope(ad.Envelope)
		} else {
			after := selector.Snapshot()
			if after.BindingHash != before.BindingHash {
				rt.recordSelectionTiming(started, before, after, "route", err, workBefore)
			}
		}
		return ad, err
	}
	return nil
}

type deterministicBrain struct {
	mu                     sync.Mutex
	persona, model, prompt string
	history                []brain.Turn
	calls                  int
	closed                 bool
}

func (b *deterministicBrain) ThinkStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil, fmt.Errorf("deterministic brain is closed")
	}
	b.calls++
	content := input
	if opts.OriginalInput != "" {
		content = opts.OriginalInput
	}
	b.history = append(b.history, brain.Turn{Role: "user", Content: content, RoutedInput: input, Speaker: opts.Speaker})
	answer := fmt.Sprintf("%s remembers the blue comet.", b.persona)
	if !strings.Contains(strings.ToLower(input), "fact") && !strings.Contains(strings.ToLower(input), "remember") && !strings.Contains(strings.ToLower(input), "comet") {
		answer = fmt.Sprintf("%s is ready.", b.persona)
	}
	id := brain.TurnIdentity{}
	if opts.Identity != nil {
		id = *opts.Identity
	}
	b.history = append(b.history, brain.Turn{Role: "assistant", Content: answer, PersonaID: id.PersonaID, PersonaDisplayName: id.PersonaDisplayName, PromptHash: id.PromptHash, ResponseID: id.ResponseID})
	b.mu.Unlock()
	chunks := make(chan string, 1)
	done := make(chan brain.StreamResult, 1)
	chunks <- answer
	close(chunks)
	done <- brain.StreamResult{}
	close(done)
	return &brain.Stream{Chunks: chunks, Done: done}, nil
}
func (b *deterministicBrain) ThinkFull(ctx context.Context, input string, opts brain.StreamOptions) (string, error) {
	s, err := b.ThinkStream(ctx, input, opts)
	if err != nil {
		return "", err
	}
	var out string
	for x := range s.Chunks {
		out += x
	}
	<-s.Done
	return out, nil
}
func (b *deterministicBrain) ClearHistory() { b.mu.Lock(); b.history = nil; b.mu.Unlock() }
func (b *deterministicBrain) History() []brain.Turn {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]brain.Turn(nil), b.history...)
}
func (b *deterministicBrain) LoadHistory(t []brain.Turn) { _ = b.ImportHistory(t) }
func (b *deterministicBrain) ImportHistory(t []brain.Turn) error {
	b.mu.Lock()
	b.history = append([]brain.Turn(nil), t...)
	b.mu.Unlock()
	return nil
}
func (b *deterministicBrain) FreezePersonaPrompt() string { return b.prompt }
func (b *deterministicBrain) count() int                  { b.mu.Lock(); defer b.mu.Unlock(); return b.calls }
func (b *deterministicBrain) Close() error                { b.mu.Lock(); b.closed = true; b.mu.Unlock(); return nil }

func newDeterministicBrain(cfg *config.Config) brain.Provider {
	id := cfg.ActivePersona
	if id == "" {
		id = cfg.Persona
	}
	if id == "" {
		id = "obey"
	}
	prompt := "prompt-" + id
	if hash, err := brain.PersonaPromptHash(cfg); err == nil {
		prompt = hash
	}
	return &deterministicBrain{persona: id, model: cfg.OllamaModel, prompt: prompt}
}

func writeSelfTestFixture(root string) (string, string, error) {
	prompts, personas := filepath.Join(root, "prompts", "persona"), filepath.Join(root, "personas")
	if err := os.MkdirAll(prompts, 0o700); err != nil {
		return "", "", err
	}
	for _, id := range []string{"samantha", "santa"} {
		if err := os.MkdirAll(filepath.Join(personas, id), 0o700); err != nil {
			return "", "", err
		}
		displayName := map[string]string{"samantha": "Samantha", "santa": "Santa"}[id]
		profile := fmt.Sprintf("schema: festival-voice.persona.v1\nid: %s\ndisplay_name: %s\naliases: [%s]\nbrain:\n  provider: ollama\n  model: fake-%s\ntts:\n  provider: none\nprompts:\n  persona: %s\n", id, displayName, id, id, id)
		if err := os.WriteFile(filepath.Join(personas, id, "persona.yaml"), []byte(profile), 0o600); err != nil {
			return "", "", err
		}
		doc := fmt.Sprintf("schema: obey-voice.prompt.v1\nprompt:\n  name: %s\n  kind: persona\n  system_prompt: You are %s.\n", id, id)
		if err := os.WriteFile(filepath.Join(prompts, id+".yaml"), []byte(doc), 0o600); err != nil {
			return "", "", err
		}
	}
	cfgPath := filepath.Join(root, "config.yaml")
	cfg := fmt.Sprintf("brain_provider: ollama\nollama_model: fake-samantha\nollama_host: http://127.0.0.1:1\nactive_persona: samantha\npersona: samantha\nprompts_dir: %s\nvoice_worker_provider: stub\nvoice_work_enabled: true\nvoice_tools_enabled: false\nvoice_router_tools: route_only\ntts_provider: none\nwork_dir: %s\n", filepath.Join(root, "prompts"), filepath.Join(root, "work"))
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		return "", "", err
	}
	return cfgPath, personas, nil
}

func wirePersonaSelfTest(ctx context.Context, eng *engine) int {
	root, err := os.MkdirTemp(eng.runDir, "fixture-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "convoflow: self-test fixture: %v\n", err)
		return 1
	}
	cfgPath, dir, err := writeSelfTestFixture(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "convoflow: self-test fixture: %v\n", err)
		return 1
	}
	base, err := loadFixtureConfig(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "convoflow: self-test config: %v\n", err)
		return 1
	}
	catalog := persona.Catalog{Dir: dir}
	initial, err := catalog.ResolveBinding(base, "samantha")
	if err != nil {
		fmt.Fprintf(os.Stderr, "convoflow: self-test persona: %v\n", err)
		return 1
	}
	bus := events.NewBus()
	bus.SubscribeAll(eng.rec.add)
	first := newDeterministicBrain(initial.Config()).(*deterministicBrain)
	var brains = []*deterministicBrain{first}
	worker := &deterministicBrain{persona: "worker", model: "worker", prompt: "worker"}
	agent, cleanup, err := voiceagent.New(ctx, voiceagent.Options{Config: initial.Config(), Events: bus, Brain: first, Worker: worker, TextOnly: true, Silent: true})
	if err != nil {
		fmt.Fprintf(os.Stderr, "convoflow: self-test agent: %v\n", err)
		return 1
	}
	r := &liveRunner{agent: agent, cleanup: cleanup, liveProvider: first, live: &observing{inner: first}, workerProvider: worker, worker: &observing{inner: worker}, player: eng.player}
	r.workerFactory = func(cfg *config.Config) (brain.Provider, error) {
		return &deterministicBrain{persona: "worker", model: "worker", prompt: "worker"}, nil
	}
	if err := configurePersonaWithFactory(r, base, catalog, initial, eng.runDir, func(cfg *config.Config) (brain.Provider, error) {
		b := newDeterministicBrain(cfg).(*deterministicBrain)
		brains = append(brains, b)
		return b, nil
	}); err != nil {
		r.close()
		fmt.Fprintf(os.Stderr, "convoflow: self-test selector: %v\n", err)
		return 1
	}
	r.persona.callCount = func() int {
		n := 0
		for _, b := range brains {
			n += b.count()
		}
		return n
	}
	r.persona.invocationSnapshot = func() any {
		out := make([]map[string]any, 0, len(brains))
		for _, b := range brains {
			out = append(out, map[string]any{"persona": b.persona, "model": b.model, "calls": b.count()})
		}
		return out
	}
	eng.runner = r
	eng.profile = runProfile{Profile: "persona-self-test", LiveProvider: "fake", LiveModel: "fake", WorkerProvider: "fake", Input: eng.scn.Input, TTSProvider: "none"}
	return 0
}
