package pipeline

import (
	"context"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// HostedNotice is a host-authored line telling the user that a persona's
// backend is hosted. It is shown as an info line, and spoken when Spoken is
// set. It is never part of the conversation: no model receives it and no
// persona is recorded as having said it.
type HostedNotice struct {
	Text   string
	Spoken bool
}

// announceHosted delivers the pending hosted-backend notice for the admitted
// response, if any, before the request that would send conversation text.
// It reports the context error when the turn was stopped meanwhile, so the
// request is not sent.
func (p *Pipeline) announceHosted(ctx context.Context, metrics *turnMetrics, turn *turnConductor) error {
	if p.HostedNotice == nil || metrics == nil || metrics.response.ResponseID == "" {
		return nil
	}
	notice, ok := p.HostedNotice(metrics.response)
	if !ok {
		return nil
	}
	p.emit(events.Info{Message: notice.Text})
	if notice.Spoken && turn != nil {
		first := metrics.firstSegment
		p.speakBestEffort(ctx, notice.Text, metrics, turn)
		metrics.firstSegment = first
		turn.to(TurnThinking)
	} else if notice.Spoken {
		_ = p.speakStandalone(ctx, nil, nil, notice.Text)
	}
	return ctx.Err()
}
