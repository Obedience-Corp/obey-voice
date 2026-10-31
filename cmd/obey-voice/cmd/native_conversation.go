//go:build !integration

package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	personaruntime "github.com/Obedience-Corp/obey-voice/internal/persona/runtime"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/nativeaudio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/session"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/stt"
)

// conversationAudio owns recordings across native/Standard replacements. Quiet
// preparation never opens capture; the existing New constructor owns devices.
// Close runs only after the foreground, caption and replay producers have joined.
type conversationAudio struct {
	lifetime context.Context
	media    *chatmedia.AudioSession
	disk     chatmedia.Store
	cache    *nativeaudio.SupportCache
	restored *session.AudioRestoration
	initial  *voiceagent.NativeCandidate
}

func newConversationAudio(ctx context.Context, store *session.Store, saved *session.Session) (*conversationAudio, error) {
	if store == nil || saved == nil {
		return nil, errors.New("conversation recordings require session/store identity")
	}
	disk, err := store.AudioMedia(saved.ID)
	if err != nil {
		return nil, err
	}
	host := &conversationAudio{lifetime: ctx, media: chatmedia.NewAudioSession(), disk: disk, cache: nativeaudio.NewSupportCache()}
	for _, turn := range saved.Turns {
		if turn.Utterance == nil {
			continue
		}
		restored, err := store.RestoreAudio(ctx, saved.ID, host.media)
		if err != nil {
			host.close()
			return nil, err
		}
		host.restored = restored
		if restored.ContextError != nil || restored.PersistenceUnavailable {
			host.close()
			return nil, fmt.Errorf("saved recording context is unavailable; restore retained recordings or start a new conversation: %w", errors.Join(restored.ContextError, brain.ErrAudioProjection))
		}
		break
	}
	return host, nil
}
func (h *conversationAudio) close() {
	if h == nil {
		return
	}
	if h.initial != nil {
		_ = h.initial.Close()
	}
	if h.restored != nil {
		h.restored.Close()
	}
	h.media.Close()
}
func (h *conversationAudio) prepare(ctx context.Context, cfg *config.Config, mode persona.Audio, publish func(context.Context, func() error) error) (*voiceagent.NativeCandidate, error) {
	return voiceagent.PrepareNativeCandidate(ctx, cfg, voiceagent.NativeCandidateOptions{Lifetime: h.lifetime, Media: h.media, Store: &h.disk, Cache: h.cache, Controls: mode.Controls, Transcript: mode.Transcript, RetainAudio: mode.RetainAudio, PublishCaption: publish})
}
func (h *conversationAudio) authorize(ctx context.Context, candidate *voiceagent.NativeCandidate) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if candidate == nil || candidate.Policy == nil || candidate.Policy.Media != h.media || !candidate.Setup.ProtocolReady {
		return errors.New("native binding does not own this conversation's verified resources")
	}
	return candidate.ValidatePrepared(ctx)
}
func (h *conversationAudio) hooks(p *pipeline.Pipeline, textOnly, silent bool) *personaruntime.NativeSelectionHooks {
	return &personaruntime.NativeSelectionHooks{TextOnly: textOnly, Silent: silent,
		Prepare: func(ctx context.Context, cfg *config.Config, mode persona.Audio, _ events.PersonaSelection, publish func(context.Context, func() error) error) (*voiceagent.NativeCandidate, error) {
			return h.prepare(ctx, cfg, mode, publish)
		},
		Authorize: func(ctx context.Context, binding *persona.SessionBinding, candidate *voiceagent.NativeCandidate) error {
			if err := h.authorize(ctx, candidate); err != nil {
				return err
			}
			mode := binding.AudioConfig()
			if err := candidate.ValidateBinding(binding.Config(), mode.Controls, mode.Transcript, mode.RetainAudio); err != nil {
				return err
			}
			return candidate.ValidateScope(p.ConversationScope())
		},
		PrepareInput: func(ctx context.Context, cfg *config.Config, candidate *voiceagent.NativeCandidate) (pipeline.InputBinding, func(), error) {
			if textOnly {
				next := pipeline.InputBinding{TextOnly: true}
				if candidate != nil {
					next.Native = candidate.Policy
				}
				return next, nil, nil
			}
			detector, ok := p.VAD.(*audio.VAD)
			if !ok || detector == nil {
				return pipeline.InputBinding{}, nil, errors.New("enable VAD in Settings and restart voice mode before selecting native audio")
			}
			if candidate != nil {
				source, ok := p.Capture.(audio.FrameSource)
				if !ok {
					return pipeline.InputBinding{}, nil, errors.New("this capture source cannot preserve finalized native recording ranges")
				}
				input, err := candidate.InputFactory(source, detector)
				return pipeline.InputBinding{Input: input, Native: candidate.Policy}, nil, err
			}
			source, ok := p.Capture.(interface{ Read() []float32 })
			if !ok {
				return pipeline.InputBinding{}, nil, errors.New("this capture source cannot provide Standard recognition")
			}
			if err := config.EnsureRuntimeAssets(ctx, cfg, config.AssetRequest{NeedSTT: true}, nil); err != nil {
				return pipeline.InputBinding{}, nil, err
			}
			recognizer, cleanup, err := stt.NewProvider(cfg, source, detector)
			return pipeline.InputBinding{STT: recognizer}, cleanup, err
		},
	}
}

func (h *conversationAudio) build(ctx context.Context, binding *persona.SessionBinding, bus *events.Bus, textOnly, silent bool, voices *voiceagent.LiveTTSManager, stage func(string)) (*pipeline.Pipeline, func(), error) {
	cfg := binding.Config()
	if binding.AudioConfig().Mode != persona.AudioNative {
		p, cleanup, err := buildPipeline(ctx, cfg, bus, textOnly, silent, voices, stage)
		if err == nil {
			p.SetOutputMuted(silent)
		}
		return p, cleanup, err
	}
	stage("Checking native audio")
	mode := binding.AudioConfig()
	candidate, err := h.prepare(ctx, cfg, mode, func(context.Context, func() error) error { return errors.New("native binding is not published yet") })
	if err != nil {
		return nil, nil, fmt.Errorf("native setup unavailable; retry setup or select Standard mode: %w", err)
	}
	h.initial = candidate
	agent, cleanup, err := candidate.NewAgent(ctx, voiceagent.Options{Events: bus, TextOnly: textOnly, Silent: silent, LiveTTS: voices, Stage: stage, Logf: func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }}, h.authorize)
	if err != nil {
		return nil, nil, err
	}
	agent.SetOutputMuted(silent)
	return agent.Pipeline, cleanup, nil
}
