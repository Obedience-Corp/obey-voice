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
	metrics.input = input
	metrics.inputOwner = p.Native.Media
	metrics.inputPolicy = p.Native
	if err := input.Validate(); err != nil {
		return "", err
	}
	if err := p.rememberAudioMedia(p.Native.Media); err != nil {
		return "", err
	}
	if err := p.prepareNativeForeground(ctx); err != nil {
		return "", err
	}
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
	if policy.RetainAudio && policy.Store == nil {
		return "", errors.New("native recording retention has no session media store")
	}
	metrics.audioRetained = policy.RetainAudio
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

// Publish recordings only after an admitted input actually belongs to canonical
// history, and before autosave can write a durable retained reference. Rejected
// admission/failed pre-dispatch input never creates an unreferenced WAV.
func (p *Pipeline) persistCanonicalNativeInput(ctx context.Context, metrics *turnMetrics) error {
	if metrics == nil || metrics.input == nil || !metrics.audioRetained || metrics.retentionWritten {
		return nil
	}
	policy := metrics.inputPolicy
	if policy == nil || policy.Store == nil || metrics.inputOwner == nil {
		return errors.New("retained input has no owning media store")
	}
	found := false
	if p.Brain != nil {
		for _, turn := range p.Brain.History() {
			if turn.Utterance != nil && turn.Utterance.ID == metrics.input.ID && turn.Utterance.CaptureEpoch == metrics.input.CaptureEpoch && turn.Utterance.Audio != nil && *turn.Utterance.Audio == *metrics.input.Audio {
				found = true
				break
			}
		}
	}
	if !found {
		return nil
	}
	if ctx.Err() != nil {
		if policy.Lifetime == nil {
			return ctx.Err()
		}
		ctx = policy.Lifetime
	}
	if err := metrics.inputOwner.PersistAudio(ctx, *policy.Store, *metrics.input.Audio); err != nil {
		return err
	}
	metrics.retentionWritten = true
	return nil
}
