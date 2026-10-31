package pipeline

import (
	"context"
	"errors"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
)

func (p *Pipeline) resolveNativeInput(ctx context.Context, input *utterance.Finalized, metrics *turnMetrics) (string, error) {
	if input == nil || p.Native == nil || p.Native.Media == nil {
		return "", errors.New("native input binding is unavailable")
	}
	input = input.Clone()
	if err := input.Validate(); err != nil {
		return "", err
	}
	metrics.input = input
	metrics.inputOwner = p.Native.Media
	metrics.inputPolicy = p.Native
	policy := p.Native
	required := policy.Controls == "standard" || policy.Transcript == "required"
	if policy.Controls != "standard" && policy.Controls != "conversation" {
		return "", errors.New("native controls policy is invalid")
	}
	if policy.Controls == "standard" && policy.Transcript == "off" {
		return "", errors.New("standard spoken controls require transcription")
	}
	if policy.Transcript == "off" {
		input.Transcript = utterance.Transcript{State: utterance.Disabled}
	}
	if required {
		if policy.Client == nil || policy.Scheduler == nil {
			return "", errors.New("native transcription binding is unavailable")
		}
		err := policy.Scheduler.Run(ctx, func(ctx context.Context) error {
			transcript, err := policy.Client.Transcribe(ctx, *input.Audio, policy.Media)
			if err == nil {
				input.Transcript = transcript
			}
			return err
		})
		if err != nil {
			return "", err
		}
	}
	if policy.RetainAudio {
		if policy.Store == nil {
			return "", errors.New("native recording retention has no session media store")
		}
		if err := policy.Media.PersistAudio(ctx, *policy.Store, *input.Audio); err != nil {
			return "", err
		}
		metrics.audioRetained = true
	}
	if input.Transcript.State == utterance.Ready {
		return input.Transcript.Text, nil
	}
	return "", nil
}

func (p *Pipeline) releaseUncommittedInput(metrics *turnMetrics) {
	if metrics.input == nil || metrics.input.Audio == nil || metrics.inputOwner == nil {
		return
	}
	if p.Brain != nil {
		for _, turn := range p.Brain.History() {
			if turn.Utterance != nil && turn.Utterance.ID == metrics.input.ID && turn.Utterance.Audio != nil && *turn.Utterance.Audio == *metrics.input.Audio {
				return
			}
		}
	}
	metrics.inputOwner.Release(*metrics.input.Audio)
}
