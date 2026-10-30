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
	if o.audioResolver != nil && o.cfg != nil {
		if err := nativeAuthentication(o.cfg.OllamaHost); err != nil {
			return err
		}
	}
	projection := make(map[string][]byte)
	used := 0
	load := func(input *utterance.Finalized) ([]byte, error) {
		if input == nil || input.Audio == nil {
			return nil, utterance.ErrInvalidAudio
		}
		if data := projection[input.Audio.ID]; data != nil {
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
		return data, nil
	}
	for i, turn := range o.working {
		if turn.Role != "user" || turn.Utterance == nil {
			continue
		}
		data, err := load(turn.Utterance)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if turn.Content == "" && turn.Utterance.Transcript.State != utterance.Ready {
				return fmt.Errorf("utterance %s: %w", turn.Utterance.ID, ErrAudioProjection)
			}
			if i < len(o.history) {
				o.history[i].Images = nil
				if o.history[i].Content == "" {
					o.history[i].Content = turn.Utterance.Transcript.Text
				}
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
