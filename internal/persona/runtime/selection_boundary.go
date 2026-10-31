package personaruntime

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
)

func (s *Selector) selectAtBoundaryWithCommitCheck(ctx context.Context, id, reason string, preCommit func(context.Context) error) (events.PersonaSelection, error) {
	if s.closed {
		return events.PersonaSelection{}, errors.New("persona selection is closed")
	}
	if err := ctx.Err(); err != nil {
		return events.PersonaSelection{}, err
	}
	s.generation++
	generation := s.generation
	p := s.opts.Pipeline
	oldBrain, scope := p.Brain, p.ConversationScope()
	outgoingNative := s.nativeCandidate
	if p.ConversationRetired() {
		return events.PersonaSelection{}, errors.New("persona selection belongs to a closed conversation")
	}
	var binding *persona.SessionBinding
	var cfg *config.Config
	var hash string
	var next, previous events.PersonaSelection
	var nextBrain, worker brain.Provider
	var voice *voiceagent.TTSSet
	var prepared *pipeline.PreparedBrainReplacement
	var inputPrepared *pipeline.PreparedInputReplacement
	var native *voiceagent.NativeCandidate
	var inputCleanup func()
	token := rand.Text()
	var turns []brain.Turn
	var err, workerErr, durabilityErr error
	unchanged, installed := false, false
	defer func() {
		if prepared != nil {
			_ = prepared.Discard()
		}
		if inputPrepared != nil {
			inputPrepared.Discard()
		}
		if !installed {
			if native != nil {
				_ = native.Close()
			} else {
				closeBrain(nextBrain)
			}
			if inputCleanup != nil {
				inputCleanup()
			}
			closeBrain(worker)
			voice.Close()
		}
	}()
	err = RunSelectionFlow(ctx, SelectionActions{
		Resolve: func(ctx context.Context) error {
			if p.Agency != nil {
				if workers := p.Agency.WorkerSettings(); workers != nil {
					s.opts.Base = s.opts.Base.Clone()
					s.opts.Base.VoiceWorkers = workers
				}
			}
			previous = s.Snapshot()
			currentScope := s.opts.Pipeline.ConversationScope()
			if s.opts.Native != nil && (!currentScope.Valid() || currentScope.SessionID != s.opts.SessionID) {
				return errors.New("native selection requires a live matching conversation scope")
			}
			if s.opts.UseDefaults && reason == "response_boundary" && id == s.opts.Initial.PersonaID {
				binding, err = persona.ResolveDefaultsBinding(s.opts.Base)
				if err != nil {
					err = fmt.Errorf("resolve persona %q: %w", id, &persona.ProfileLoadError{ID: id, Err: err})
				}
			} else {
				var issues []persona.CatalogIssue
				if s.opts.Native != nil {
					binding, issues, err = s.opts.Catalog.ResolveCandidateBindingWithIssues(s.opts.Base, id)
				} else {
					binding, issues, err = s.opts.Catalog.ResolveBindingWithIssues(s.opts.Base, id)
				}
				s.warnCatalogIssues(issues)
			}
			if err != nil {
				// A malformed edit to the currently live profile must not brick
				// nameless turns. A missing profile is different: restored or
				// deleted identities stay fail-closed until explicitly selected.
				if reason == "response_boundary" && id == previous.PersonaID && canKeepLastGood(s, previous, currentScope, err) {
					s.warnCatalogIssues([]persona.CatalogIssue{{ID: id, Err: err}})
					binding = s.binding
					cfg = binding.Config()
					hash = previous.PromptHash
					next = previous
					unchanged = true
					return nil
				}
				return err
			}
			cfg = binding.Config()
			hash, err = brain.PersonaPromptHash(cfg)
			if err != nil {
				// Persona prompt edits use the same last-good rule as the
				// profile reloader. This branch is before preparation and
				// therefore cannot hide a persistence or publication failure.
				if reason == "response_boundary" && id == previous.PersonaID && canKeepLastGoodBoundary(s, previous, currentScope) {
					s.warnCatalogIssues([]persona.CatalogIssue{{ID: id, Err: err}})
					binding = s.binding
					cfg = binding.Config()
					hash = previous.PromptHash
					next = previous
					unchanged = true
					return nil
				}
				return err
			}
			next = s.snapshotFor(binding, hash, generation, reason)
			if !s.requiresSelection && next.SessionID == previous.SessionID && next.BindingHash == previous.BindingHash && next.ConversationEpoch == previous.ConversationEpoch {
				unchanged = true
				return nil
			}
			if cfg.BrainProvider != "ollama" && cfg.BrainProvider != "codex" {
				return fmt.Errorf("persona %q: provider %q has no verified shared history projection", id, cfg.BrainProvider)
			}

			return nil
		},
		Prepare: func(ctx context.Context) error {
			if unchanged {
				return nil
			}
			if s.opts.Native != nil {
				if err = p.QuiesceNative(ctx); err != nil {
					return err
				}
				inputPrepared, err = p.PrepareInputReplacement(ctx, p.InputSnapshot(s.opts.Native.TextOnly))
				if err != nil {
					return err
				}
			}
			if binding.AudioConfig().Mode == persona.AudioNative {
				if s.opts.Native == nil {
					return persona.ErrNativeAudioUnavailable
				}
				native, err = prepareResource(ctx, func() (*voiceagent.NativeCandidate, error) {
					return s.opts.Native.Prepare(ctx, cfg, binding.AudioConfig(), next, s.captionPublisher(token, next))
				}, func(c *voiceagent.NativeCandidate) {
					if c != outgoingNative {
						_ = c.Close()
					}
				})
				if err != nil {
					return err
				}
				if native != nil && native == outgoingNative {
					native = nil
					return errors.New("native preparation returned the live binding instead of a fresh candidate")
				}
				if native == nil || native.Brain == nil || native.Voice == nil || native.Policy == nil {
					return errors.New("native candidate is incomplete")
				}
				if err := native.BindScope(scope); err != nil {
					return err
				}
				if native.PreparedConfig() != nil {
					mode := binding.AudioConfig()
					if err := native.ValidateBinding(cfg, mode.Controls, mode.Transcript, mode.RetainAudio); err != nil {
						return err
					}
				}
				if s.opts.Native.Authorize == nil {
					return ErrNativeUnqualified
				}
				if err = s.opts.Native.Authorize(ctx, binding, native); err != nil {
					return err
				}
				nextBrain = native.Brain
				if !s.opts.Native.Silent {
					voice = native.Voice
				}
			} else {
				nextBrain, err = prepareResource(ctx, func() (brain.Provider, error) { return s.opts.NewBrain(ctx, cfg) }, closeBrain)
			}
			if err != nil {
				return fmt.Errorf("prepare persona brain: %w", err)
			}
			projection, ok := nextBrain.(brain.HistoryProjectionProvider)
			if !ok {
				return errors.New("persona brain does not support shared history projection")
			}
			freezer, ok := nextBrain.(brain.PersonaPromptFreezer)
			if !ok {
				return errors.New("persona brain cannot pin response identity")
			}
			if freezer.FreezePersonaPrompt() != hash {
				return errors.New("persona prompt changed while preparing; retry selection")
			}
			turns = oldBrain.History()
			if canonical, ok := oldBrain.(brain.CanonicalHistoryProvider); ok {
				turns = canonical.CanonicalHistory()
			}
			if native != nil {
				nativeView := turns
				if source, ok := oldBrain.(brain.ContinuityTransferProvider); ok {
					nativeView = source.ContinuitySnapshot().Working
				}
				if err := brain.ValidateNativeProjection(nativeView); err != nil {
					return err
				}
			}
			if native == nil && s.opts.Native != nil {
				textView := turns
				if source, ok := oldBrain.(brain.ContinuityTransferProvider); ok {
					textView = source.ContinuitySnapshot().Working
				}
				if err := brain.ValidateTextProjection(textView); err != nil {
					return fmt.Errorf("native history needs a ready transcript or explicit summary before Standard mode: %w", err)
				}
			}
			if source, ok := oldBrain.(brain.ContinuityTransferProvider); ok {
				if target, ok := nextBrain.(brain.ContinuityTransferProvider); ok {
					if err := target.ImportContinuity(source.ContinuitySnapshot()); err != nil {
						return fmt.Errorf("transfer conversation continuity: %w", err)
					}
				} else if err := projection.ImportHistory(turns); err != nil {
					return fmt.Errorf("project conversation: %w", err)
				}
			} else if err := projection.ImportHistory(turns); err != nil {
				return fmt.Errorf("project conversation: %w", err)
			}
			if s.opts.Voices != nil && native == nil && (s.opts.Native == nil || !s.opts.Native.Silent) {
				voice, err = prepareResource(ctx, func() (*voiceagent.TTSSet, error) { return s.opts.NewVoice(ctx, cfg) }, func(v *voiceagent.TTSSet) { v.Close() })
				if err != nil {
					return fmt.Errorf("prepare persona voice: %w", err)
				}
				if voice == nil || voice.Primary == nil || !voice.Primary.Available() {
					return errors.New("persona voice is unavailable")
				}
				if voice.FallbackWarning != nil || voice.VoiceNotice != nil {
					return fmt.Errorf("persona voice cannot be bound as configured: %w", errors.Join(voice.FallbackWarning, voice.VoiceNotice))
				}
			}
			if native != nil && s.opts.Voices == nil {
				return errors.New("native selection requires managed voice replacement")
			}
			if inputPrepared != nil {
				var nextInput pipeline.InputBinding
				nextInput, inputCleanup, err = s.opts.Native.PrepareInput(ctx, cfg, native)
				if err != nil {
					return err
				}
				if err = inputPrepared.Stage(nextInput); err != nil {
					return err
				}
			}
			if p.Agency != nil {
				if workers := p.Agency.WorkerSettings(); workers != nil {
					cfg.VoiceWorkers = workers
				}
				if cfg.VoiceWorkers == nil || cfg.VoiceWorkers.Mode != config.WorkerModeTiered {
					worker, workerErr = prepareResource(ctx, func() (brain.Provider, error) { return s.opts.NewWorker(ctx, cfg) }, closeBrain)
				}
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			prepared, err = p.PrepareConversationBrain(nextBrain, worker, cfg, workerErr)
			if err != nil {
				return err
			}

			return nil
		},
		Commit: func(ctx context.Context) error {
			if preCommit != nil {
				if err := preCommit(ctx); err != nil {
					return err
				}
			}
			if unchanged {
				if reason != "response_boundary" {
					s.opts.UseDefaults = false
				}
				return nil
			}
			if native != nil {
				if err := native.ValidateScope(p.ConversationScope()); err != nil {
					return err
				}
				if err := native.ValidatePrepared(ctx); err != nil {
					return err
				}
			}
			if inputPrepared != nil {
				if err := inputPrepared.Validate(); err != nil {
					return err
				}
			}
			s.publicationMu.Lock()
			publicationLocked := true
			defer func() {
				if publicationLocked {
					s.publicationMu.Unlock()
				}
			}()
			next.Revision = previous.Revision + 1
			persist := func() error {
				if err := ctx.Err(); err != nil {
					return err
				}
				if generation != s.generation || oldBrain != p.Brain || scope != p.ConversationScope() || p.ConversationRetired() {
					return errors.New("persona selection belongs to a retired conversation boundary")
				}
				err := s.opts.Persist(turns, next)
				if session.WriteCommitted(err) {
					// The file already contains this selection. Install the same
					// binding and report uncertainty without pretending rollback.
					durabilityErr = err
					return nil
				}
				return err
			}
			commit := func() error { return prepared.Commit(persist) }
			if voice != nil {
				err = s.opts.Voices.ReplaceTransaction(p, voice, commit)
			} else {
				err = commit()
			}
			if err != nil {
				return fmt.Errorf("commit persona selection: %w", err)
			}
			if inputPrepared != nil {
				inputPrepared.Publish()
			}
			oldNative, oldInputCleanup := s.nativeCandidate, s.inputCleanup
			s.nativeCandidate, s.inputCleanup = native, inputCleanup
			if native != nil {
				native.TransferBrainOwnership()
			}
			s.bindingToken = token
			installed = true
			s.opts.Base.ConversationDirectories = cfg.ConversationDirectories.Clone()
			if reason != "response_boundary" {
				s.opts.UseDefaults = false
			}
			s.mu.Lock()
			s.binding, s.selected = binding, next
			s.mu.Unlock()
			s.requiresSelection = false
			s.publicationMu.Unlock()
			publicationLocked = false
			if oldNative != nil {
				_ = oldNative.Close()
			}
			if oldInputCleanup != nil {
				oldInputCleanup()
			}
			if p.Events != nil {
				p.Events.Emit(next)
			}
			s.warnPersistence(durabilityErr)

			return nil
		},
	})
	if err != nil {
		return events.PersonaSelection{}, err
	}
	if unchanged {
		return previous, nil
	}
	return next, nil
}
