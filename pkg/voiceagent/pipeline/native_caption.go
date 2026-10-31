package pipeline

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/nativeaudio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/utterance"
)

func (p *Pipeline) prepareNativeForeground(ctx context.Context) error {
	policy := p.Native
	if policy == nil {
		return nil
	}
	if policy.retired.Load() {
		return errors.New("native audio binding was retired; prepare it again")
	}
	if policy.Scheduler == nil {
		return nil
	}
	if err := policy.Scheduler.CancelCaption(ctx); err != nil {
		return err
	}
	policy.captionMu.Lock()
	ticket := policy.caption
	policy.caption = nil
	policy.captionMu.Unlock()
	if ticket != nil && !ticket.completed.Load() {
		return ticket.publish(ctx, utterance.Transcript{State: utterance.Unavailable, Provider: "ollama", Model: policy.Client.Model(), Reason: "superseded by another request"})
	}
	return nil
}

// Retire cancels owned inference after successful replacement or shutdown.
// Candidate preparation must only cancel/join captions, so rollback can resume.
func (n *NativePolicy) Retire(ctx context.Context) error {
	n.retired.Store(true)
	if n.Client != nil {
		n.Client.Close()
	}
	if n.Scheduler != nil {
		return n.Scheduler.CancelCaption(ctx)
	}
	return nil
}
func (p *Pipeline) finishNativeCaption(ctx context.Context, metrics *turnMetrics) {
	if metrics.captionEligible && ctx.Err() == nil {
		p.scheduleNativeCaption(metrics)
		return
	}
	input, policy := metrics.input, metrics.inputPolicy
	if input == nil || policy == nil || policy.Controls != "conversation" || policy.Transcript != "auto" || input.Transcript.State != utterance.Pending || policy.retired.Load() || policy.PublishCaption == nil || policy.Lifetime == nil || policy.Client == nil {
		return
	}
	updater, ok := p.Brain.(brain.InputTranscriptUpdater)
	if !ok {
		return
	}
	reason := "response did not complete"
	if ctx.Err() != nil {
		reason = "response interrupted"
	}
	ticket := &nativeCaptionTicket{policy: policy, input: input.Clone(), updater: updater, pipeline: p, metrics: metrics}
	commitCtx, cancel := context.WithTimeout(policy.Lifetime, nativeaudio.CaptionJoinTimeout)
	defer cancel()
	_ = ticket.publish(commitCtx, utterance.Transcript{State: utterance.Unavailable, Provider: "ollama", Model: policy.Client.Model(), Reason: reason})
}

func (p *Pipeline) scheduleNativeCaption(metrics *turnMetrics) {
	input, policy := metrics.input, metrics.inputPolicy
	if input == nil || policy == nil || policy.Controls != "conversation" || policy.Transcript != "auto" || input.Transcript.State != utterance.Pending || policy.retired.Load() {
		return
	}
	if policy.Client == nil || policy.Scheduler == nil || policy.Lifetime == nil || policy.PublishCaption == nil {
		return
	}
	updater, ok := p.Brain.(brain.InputTranscriptUpdater)
	if !ok {
		return
	}
	// A rejected/noncanonical input is not kept alive for optional transcription.
	found := false
	for _, turn := range p.Brain.History() {
		if turn.Utterance != nil && turn.Utterance.ID == input.ID && turn.Utterance.CaptureEpoch == input.CaptureEpoch {
			found = true
			break
		}
	}
	if !found {
		return
	}
	input = input.Clone()
	if err := policy.Media.Retain(*input.Audio); err != nil {
		return
	}
	ticket := &nativeCaptionTicket{policy: policy, input: input, updater: updater, pipeline: p, metrics: metrics}
	policy.captionMu.Lock()
	policy.caption = ticket
	policy.captionMu.Unlock()
	started := policy.Scheduler.StartCaption(policy.Lifetime, func(ctx context.Context) {
		defer policy.Media.Release(*input.Audio)
		transcript, err := policy.Client.Transcribe(ctx, *input.Audio, policy.Media)
		if ctx.Err() != nil || policy.retired.Load() {
			return
		}
		if err != nil {
			reason := err.Error()
			if len(reason) > 2048 {
				reason = reason[:2048]
			}
			transcript = utterance.Transcript{State: utterance.Unavailable, Provider: "ollama", Model: policy.Client.Model(), Reason: strings.ToValidUTF8(reason, "?")}
		}
		_ = ticket.publish(ctx, transcript)
	})
	if !started {
		policy.captionMu.Lock()
		if policy.caption == ticket {
			policy.caption = nil
		}
		policy.captionMu.Unlock()
		policy.Media.Release(*input.Audio)
	}
}

type nativeCaptionTicket struct {
	policy    *NativePolicy
	input     *utterance.Finalized
	updater   brain.InputTranscriptUpdater
	pipeline  *Pipeline
	metrics   *turnMetrics
	completed atomic.Bool
}

func (t *nativeCaptionTicket) publish(ctx context.Context, transcript utterance.Transcript) error {
	return t.policy.PublishCaption(ctx, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if t.policy.retired.Load() {
			return errors.New("caption binding was retired")
		}
		if err := t.updater.UpdateInputTranscript(t.input.ID, t.input.CaptureEpoch, transcript); err != nil {
			return err
		}
		t.completed.Store(true)
		t.pipeline.emitResponse(t.metrics, events.InputTranscriptUpdated{UtteranceID: t.input.ID, CaptureEpoch: t.input.CaptureEpoch, Transcript: transcript})
		if t.pipeline.OnTurn != nil {
			t.pipeline.OnTurn()
		}
		return nil
	})
}

// QuiesceNative cancels and joins optional caption inference without retiring
// the selected client. A failed candidate can therefore resume the old binding.
func (p *Pipeline) QuiesceNative(ctx context.Context) error {
	if p == nil || p.Native == nil {
		return nil
	}
	if p.Native.retired.Load() {
		if p.Native.Scheduler != nil {
			return p.Native.Scheduler.CancelCaption(ctx)
		}
		return nil
	}
	return p.prepareNativeForeground(ctx)
}
