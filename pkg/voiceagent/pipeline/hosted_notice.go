package pipeline

import (
	"context"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// HostedNotice is a host-authored line telling the user that a persona's
// backend is hosted. It is shown as an info line and never spoken. It is
// never part of the conversation: no model receives it and no persona is
// recorded as having said it.
type HostedNotice struct {
	Text string
	// Delivered records the notice as given. It is called only once the
	// notice was shown and the request it precedes goes ahead.
	Delivered func()
}

// announceHosted delivers the pending hosted-backend notice for the admitted
// response, if any, before the request that would send conversation text.
// It reports the context error when the turn was stopped meanwhile, so the
// request is not sent.
func (p *Pipeline) announceHosted(ctx context.Context, metrics *turnMetrics) error {
	if p.HostedNotice == nil || metrics == nil || metrics.response.ResponseID == "" {
		return nil
	}
	notice, ok := p.HostedNotice(metrics.response)
	if !ok {
		return nil
	}
	return p.deliverHostedNotice(ctx, notice)
}

// announceHostedRequest is announceHosted just before a turn's request, once
// any earlier request still stopping on the provider has ended.
func (p *Pipeline) announceHostedRequest(ctx context.Context, metrics *turnMetrics) error {
	if err := p.awaitProvider(ctx); err != nil {
		return err
	}
	return p.announceHosted(ctx, metrics)
}

// announceLiveHosted is announceHosted for a request the host makes for the
// live persona outside an admitted turn, such as compaction.
func (p *Pipeline) announceLiveHosted(ctx context.Context) error {
	if p.LiveHostedNotice == nil {
		return nil
	}
	notice, ok := p.LiveHostedNotice()
	if !ok {
		return nil
	}
	return p.deliverHostedNotice(ctx, notice)
}

func (p *Pipeline) deliverHostedNotice(ctx context.Context, notice HostedNotice) error {
	p.emit(events.Info{Message: notice.Text})
	if err := ctx.Err(); err != nil {
		return err
	}
	if notice.Delivered != nil {
		notice.Delivered()
	}
	return nil
}
