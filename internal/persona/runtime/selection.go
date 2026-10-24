package personaruntime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sync"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

// SelectionOptions connects persona selection to the existing conversation
// owner. Persist must atomically save the candidate selection and turns in
// that owner's normal session. It must not publish UI state itself.
type SelectionOptions struct {
	// AddressResolver overrides the current provider classifier (for alternate hosts/tests).
	AddressResolver brain.AddresseeResolver
	// UseDefaults preserves the launcher's Settings stack until an explicit
	// persona selection; the default profile still supplies identity/prompt.
	UseDefaults bool
	Base        *config.Config
	Catalog     persona.Catalog
	Pipeline    *pipeline.Pipeline
	Voices      *voiceagent.LiveTTSManager
	SessionID   string
	Initial     *persona.SessionBinding
	Persist     func([]brain.Turn, events.PersonaSelection) error
	NewBrain    func(context.Context, *config.Config) (brain.Provider, error)
	NewWorker   func(context.Context, *config.Config) (brain.Provider, error)
	NewVoice    func(context.Context, *config.Config) (*voiceagent.TTSSet, error)
}

// Selector replaces response resources at the host's serialized boundary. It
// does not schedule turns or own conversation/work lifetime. Hosts must call
// Select in request order and serialize it with clear, restore, and shutdown.
type Selector struct {
	opts              SelectionOptions
	transitionMu      sync.Mutex // joins publication before host resource cleanup
	closed            bool
	mu                sync.RWMutex // protects read-only snapshots; never held during I/O
	selected          events.PersonaSelection
	binding           *persona.SessionBinding
	generation        uint64
	requiresSelection bool // explicit restore may have no usable saved persona
}

func NewSelector(opts SelectionOptions) (*Selector, error) {
	if opts.Base == nil || opts.Pipeline == nil || opts.Pipeline.Brain == nil || opts.Initial == nil || opts.SessionID == "" || opts.Persist == nil {
		return nil, errors.New("persona selection requires base settings, pipeline, binding, session, and persistence")
	}
	if _, ok := opts.Pipeline.Brain.(brain.HistoryProjectionProvider); !ok {
		return nil, errors.New("persona selection: current provider does not support shared history projection")
	}
	freezer, ok := opts.Pipeline.Brain.(brain.PersonaPromptFreezer)
	if !ok {
		return nil, errors.New("persona selection: current provider cannot pin response identity")
	}
	if _, _, err := SelectionFlowInfo(); err != nil {
		return nil, err
	}
	opts.Base = opts.Base.Clone()
	opts.Base.WorkDir = opts.Initial.Config().WorkDir
	opts.Base.WorkDirSource = opts.Initial.Config().WorkDirSource
	opts.Base.ConversationDirectories = opts.Initial.Config().ConversationDirectories
	if opts.NewBrain == nil {
		opts.NewBrain = func(_ context.Context, cfg *config.Config) (brain.Provider, error) { return brain.NewProvider(cfg) }
	}
	if opts.NewWorker == nil {
		opts.NewWorker = func(_ context.Context, cfg *config.Config) (brain.Provider, error) {
			return brain.NewWorkerProvider(cfg)
		}
	}
	if opts.NewVoice == nil {
		opts.NewVoice = func(_ context.Context, cfg *config.Config) (*voiceagent.TTSSet, error) {
			return voiceagent.NewTTSSet(cfg)
		}
	}
	s := &Selector{opts: opts, binding: opts.Initial}
	hash := freezer.FreezePersonaPrompt()
	s.selected = s.snapshotFor(opts.Initial, hash, 0, "initial")
	if err := opts.Persist(opts.Pipeline.Brain.History(), s.selected); err != nil {
		if !session.WriteCommitted(err) {
			return nil, fmt.Errorf("persist initial persona: %w", err)
		}
		s.warnPersistence(err)
	}
	return s, nil
}

func (s *Selector) Snapshot() events.PersonaSelection {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.selected
}

func (s *Selector) Config() *config.Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.binding.Config()
}

func (s *Selector) snapshotFor(b *persona.SessionBinding, hash string, generation uint64, reason string) events.PersonaSelection {
	cfg := b.Config()
	// Installation worker tiers have their own revision and admission lifetime.
	// Saving them must not trigger a responder brain/voice reload next turn.
	responseCfg := cfg.Clone()
	responseCfg.VoiceWorkers = nil
	// Only the digest is exposed; credential-bearing config is never logged.
	encoded, _ := json.Marshal(struct {
		Config *config.Config
		Prompt string
	}{responseCfg, hash})
	digest := sha256.Sum256(encoded)
	scope := s.opts.Pipeline.ConversationScope()
	return events.PersonaSelection{
		WorkDir: cfg.WorkDir, WorkDirSource: cfg.WorkDirSource, Directories: cfg.ConversationDirectories.Clone(),
		SessionID: s.opts.SessionID, ConversationEpoch: scope.ConversationEpoch,
		PersonaID: b.PersonaID, DisplayName: b.DisplayName, PromptHash: hash, BindingHash: hex.EncodeToString(digest[:]),
		Generation: generation, BrainProvider: b.BrainProvider, BrainModel: b.BrainModel,
		VoiceProvider: cfg.TTSProvider, Voice: config.TTSVoiceFor(cfg, cfg.TTSProvider), Reason: reason,
	}
}

// Select prepares all fallible resources before durable selection changes.
// The host, not this method, supplies FIFO admission and turn exclusion.
func (s *Selector) Select(ctx context.Context, id, reason string) (events.PersonaSelection, error) {
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()
	return s.selectAtBoundary(ctx, id, reason)
}

func (s *Selector) selectAtBoundary(ctx context.Context, id, reason string) (events.PersonaSelection, error) {
	return s.selectAtBoundaryWithCommitCheck(ctx, id, reason, nil)
}

func canKeepLastGood(s *Selector, previous events.PersonaSelection, currentScope work.WorkScope, err error) bool {
	var profileErr *persona.ProfileLoadError
	if !errors.As(err, &profileErr) || errors.Is(profileErr, fs.ErrNotExist) || errors.Is(err, persona.ErrInactive) {
		return false
	}
	return canKeepLastGoodBoundary(s, previous, currentScope)
}

func canKeepLastGoodBoundary(s *Selector, previous events.PersonaSelection, currentScope work.WorkScope) bool {
	if s.binding == nil || s.requiresSelection {
		return false
	}
	if s.opts.Pipeline.ConversationRetired() || previous.SessionID != s.opts.SessionID || previous.PersonaID != s.binding.PersonaID {
		return false
	}
	if currentScope.Valid() {
		return previous.ConversationEpoch != "" && previous.ConversationEpoch == currentScope.ConversationEpoch
	}
	return previous.ConversationEpoch == currentScope.ConversationEpoch
}

func (s *Selector) warnPersistence(err error) {
	if err != nil && s.opts.Pipeline.Events != nil {
		s.opts.Pipeline.Events.Emit(events.Error{Stage: "session_durability", Message: fmt.Sprintf("Persona selection applied, but its persistence could not be confirmed: %v", err)})
	}
}

// Reload applies explicit Settings changes at the same serialized boundary.
// Failure keeps the previous base for subsequent persona inheritance.
func (s *Selector) Reload(ctx context.Context, base *config.Config) error {
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()
	if base == nil {
		return errors.New("persona settings are nil")
	}
	previous := s.opts.Base
	s.opts.Base = base.Clone()
	s.opts.Base.WorkDir, s.opts.Base.WorkDirSource = previous.WorkDir, previous.WorkDirSource
	s.opts.Base.ConversationDirectories = previous.ConversationDirectories.Clone()
	if _, err := s.selectAtBoundary(ctx, s.Snapshot().PersonaID, "response_boundary"); err != nil {
		s.opts.Base = previous
		return err
	}
	return nil
}

// Admit refreshes edited profiles and returns an immutable response identity.
func (s *Selector) Admit(ctx context.Context) (events.ResponseEnvelope, error) {
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()
	if s.requiresSelection {
		return events.ResponseEnvelope{}, errors.New("restored conversation requires a valid persona selection")
	}
	selected, err := s.selectAtBoundary(ctx, s.Snapshot().PersonaID, "response_boundary")
	if err != nil {
		return events.ResponseEnvelope{}, err
	}
	if _, participants, err := s.conversationRoster(); err == nil {
		s.setGroupParticipants(participants)
	} else {
		return events.ResponseEnvelope{}, err
	}
	return responseEnvelope(selected), nil
}

func (s *Selector) setGroupParticipants(participants []brain.GroupParticipant) {
	if group, ok := s.opts.Pipeline.Brain.(interface {
		SetGroupParticipants([]brain.GroupParticipant)
	}); ok {
		group.SetGroupParticipants(participants)
	}
}

func responseEnvelope(selected events.PersonaSelection) events.ResponseEnvelope {
	return events.ResponseEnvelope{
		SessionID: selected.SessionID, ConversationEpoch: selected.ConversationEpoch, ResponseID: rand.Text(),
		PersonaID: selected.PersonaID, DisplayName: selected.DisplayName, PromptHash: selected.PromptHash,
		BindingHash: selected.BindingHash, SelectionRevision: selected.Revision, VoiceProvider: selected.VoiceProvider, Voice: selected.Voice,
	}
}

// RestoreSession adopts history and work that the host has explicitly restored.
// It does not load history, start work, or select a fallback. Until Select
// succeeds, the transcript remains readable but response admission is closed.
// Like Select, this must run at the host's serialized lifecycle boundary.
func (s *Selector) RestoreSession(sessionID, personaID, displayName, promptHash string, directories ...*config.ConversationDirectories) {
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()
	if s.closed {
		return
	}
	if len(directories) > 0 && directories[0] != nil {
		s.opts.Base.ConversationDirectories = directories[0].Clone()
		if choice, ok := directories[0].Personas[personaID]; ok {
			s.opts.Base.WorkDir, s.opts.Base.WorkDirSource = choice.Dir, choice.Source
		}
	}
	s.generation++
	s.opts.SessionID = sessionID
	s.opts.UseDefaults = false
	s.requiresSelection = true
	s.mu.Lock()
	s.selected = events.PersonaSelection{
		WorkDir: s.opts.Base.WorkDir, WorkDirSource: s.opts.Base.WorkDirSource, Directories: s.opts.Base.ConversationDirectories.Clone(),
		SessionID: sessionID, ConversationEpoch: s.opts.Pipeline.ConversationScope().ConversationEpoch,
		PersonaID: personaID, DisplayName: displayName, PromptHash: promptHash,
		Revision: s.selected.Revision + 1, Generation: s.generation, Reason: "restore_requires_selection",
	}
	selected := s.selected
	s.mu.Unlock()
	if s.opts.Pipeline.Events != nil {
		s.opts.Pipeline.Events.Emit(selected)
	}
}

// Close joins any in-flight selection publication before the host closes its
// work, brain, and voice resources. The host cancels its run context first;
// this final join also covers a filesystem commit outliving the turn timeout.
// It does not close conversation resources or schedule turns itself.
func (s *Selector) Close() {
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()
	s.closed = true
}

func closeBrain(b brain.Provider) {
	if b != nil {
		_ = brain.CloseProvider(b)
	}
}

// A legacy synchronous constructor may finish after cancellation. Its result
// is then closed by its own goroutine and cannot reach selection publication.
func prepareResource[T any](ctx context.Context, create func() (T, error), discard func(T)) (T, error) {
	type result struct {
		value T
		err   error
	}
	ready := make(chan result)
	go func() {
		value, err := create()
		select {
		case ready <- result{value, err}:
		case <-ctx.Done():
			discard(value)
		}
	}()
	select {
	case got := <-ready:
		if got.err != nil {
			discard(got.value)
			var zero T
			return zero, got.err
		}
		return got.value, nil
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}
