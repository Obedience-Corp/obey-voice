package brain

import (
	"context"
	"errors"
	"fmt"
	"github.com/ollama/ollama/envconfig"
	"net/url"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
	"github.com/ollama/ollama/api"
)

var ErrImageProjection = errors.New("retained images need a compatible persona or an explicit new context before native audio")

var ErrAudioProjection = errors.New("audio-only history needs retained audio, a ready transcript or a valid summary")

// AudioInput is an optional capability; ordinary providers retain text/image
// signatures and cannot silently accept a spoken input they cannot represent.
type AudioInput interface {
	SetAudioResolver(utterance.Resolver)
	ValidateAudioInput(context.Context, *utterance.Finalized) error
}

func nativeAuthentication(endpoint string) error {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return err
	}
	if envconfig.UseAuth() || strings.EqualFold(parsed.Hostname(), "ollama.com") {
		return errors.New("native audio cannot resolve consistent authentication across chat, transcription and speech")
	}
	return nil
}

func (o *OllamaBrain) SetAudioResolver(resolver utterance.Resolver) { o.audioResolver = resolver }
func cloneUtterance(input *utterance.Finalized) *utterance.Finalized {
	if input == nil {
		return nil
	}
	out := *input
	if input.Audio != nil {
		ref := *input.Audio
		out.Audio = &ref
	}
	out.Spans = append([]utterance.Span(nil), input.Spans...)
	return &out
}
func (o *OllamaBrain) ValidateAudioInput(ctx context.Context, input *utterance.Finalized) error {
	if input == nil {
		return nil
	}
	_, err := o.resolveAudioInput(ctx, input)
	return err
}
func (o *OllamaBrain) resolveAudioInput(ctx context.Context, input *utterance.Finalized) ([]byte, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	if o.audioResolver == nil {
		return nil, fmt.Errorf("native audio resolver is not bound: %w", utterance.ErrAudioUnavailable)
	}
	data, err := o.audioResolver.ResolveAudio(ctx, *input.Audio)
	if err != nil {
		return nil, err
	}
	ref, canonical, err := chatmedia.CanonicalAudio(data)
	if err != nil {
		return nil, err
	}
	if ref != *input.Audio {
		return nil, utterance.ErrInvalidAudio
	}
	return canonical, nil
}

func (o *OllamaBrain) prepareAudioHistory(ctx context.Context, opts StreamOptions) error {
	if o.audioResolver != nil {
		if len(opts.Attachments) > 0 {
			return ErrImageProjection
		}
		if err := ValidateNativeProjection(o.effectiveTurns(o.working)); err != nil {
			return err
		}
	}
	if o.audioResolver != nil && o.cfg != nil {
		if err := nativeAuthentication(o.cfg.OllamaHost); err != nil {
			return err
		}
	}
	o.audioProjectionBlocked.Store(false)
	if o.cfg != nil {
		high, low := o.cfg.MaxHistory*2, o.cfg.MaxHistory
		if o.cfg.ContinuityArchiveEnabled {
			high = min(high, 12)
			low = min(low, 10)
		}
		if len(o.history) > high {
			start := historyWindowStart(o.history, low)
			if err := ValidateTextProjection(o.effectiveTurns(o.working[:min(start, len(o.working))])); err != nil {
				return err
			}
		}
	}
	projection, load := o.audioProjectionLoader(ctx)
	for i, turn := range o.working {
		turn = o.effectiveTurn(turn)
		if turn.Role != "user" || turn.Utterance == nil {
			continue
		}
		data, err := load(turn.Utterance)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if turn.Utterance.Transcript.State != utterance.Ready {
				return fmt.Errorf("utterance %s: %w", turn.Utterance.ID, ErrAudioProjection)
			}
			if i < len(o.history) {
				o.history[i].Images = nil
				o.history[i].Content = encodeOllamaUser(turn.Speaker, audioTranscriptProjection(turn))
			}
			continue
		}
		if i < len(o.history) {
			o.history[i].Images = []api.ImageData{data}
		}
	}
	if opts.Utterance != nil {
		if len(opts.Attachments) > 0 {
			return errors.New("native audio and image attachments cannot share this input")
		}
		if _, err := load(opts.Utterance); err != nil {
			return err
		}
	}
	o.audioProjection = projection
	return nil
}

func audioTranscriptProjection(turn Turn) string {
	transcript := turn.Utterance.Transcript.Text
	if turn.TypedInput == "" {
		return transcript
	}
	return "Typed instruction:\n" + turn.TypedInput + "\nTranscript of the attached recording:\n" + transcript
}

// ValidateTextProjection rejects any recording that cannot be represented by
// an actual ready transcript. Existing summary-only working views pass because
// their canonical recording evidence is not being silently reprojected here.
func ValidateTextProjection(turns []Turn) error {
	for _, turn := range turns {
		if turn.Utterance != nil && turn.Utterance.Transcript.State != utterance.Ready {
			return ErrAudioProjection
		}
	}
	return nil
}

// ValidateNativeProjection preserves image evidence instead of sending it
// through LFM3's audio Images wire field. Explicit working summaries may be
// projected, while canonical image metadata remains in the session evidence.
func ValidateNativeProjection(turns []Turn) error {
	for _, turn := range turns {
		if len(turn.Attachments) > 0 {
			return ErrImageProjection
		}
	}
	return nil
}

// Every request-private projection shares verification, deduplication and the
// session byte bound, without borrowing or mutating another request's map.
func (o *OllamaBrain) audioProjectionLoader(ctx context.Context) (map[string][]byte, func(*utterance.Finalized) ([]byte, error)) {
	projection := make(map[string][]byte)
	references := make(map[string]utterance.AudioRef)
	used := 0
	load := func(input *utterance.Finalized) ([]byte, error) {
		if input == nil || input.Audio == nil {
			return nil, utterance.ErrInvalidAudio
		}
		if err := input.Validate(); err != nil {
			return nil, err
		}
		if data := projection[input.Audio.ID]; data != nil {
			if references[input.Audio.ID] != *input.Audio {
				return nil, utterance.ErrInvalidAudio
			}
			return data, nil
		}
		data, err := o.resolveAudioInput(ctx, input)
		if err != nil {
			return nil, err
		}
		if len(data) > chatmedia.SessionAudioBudget-used {
			return nil, utterance.ErrBudgetExceeded
		}
		used += len(data)
		projection[input.Audio.ID] = data
		references[input.Audio.ID] = *input.Audio
		return data, nil
	}
	return projection, load
}
