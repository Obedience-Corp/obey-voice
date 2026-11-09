package openaispeech

import (
	"context"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

func (p *Provider) SetProviderStatusSink(sink func(context.Context, events.Info)) {
	p.statusMu.Lock()
	p.statusSink = sink
	p.statusMu.Unlock()
}

func (p *Provider) reportProviderResult(ctx context.Context, err error) {
	if ctx.Err() != nil {
		return
	}
	state := "available"
	message := "Primary speech provider recovered."
	if err != nil {
		if !tts.IsProviderErrorKind(err, tts.ProviderErrorUnavailable) && !tts.IsProviderErrorKind(err, tts.ProviderErrorWorker) && !tts.IsProviderErrorKind(err, tts.ProviderErrorMalformed) {
			return
		}
		state = "unavailable"
		message = "Primary speech provider unavailable. Replies may use the configured fallback voice; check the speech server."
	}
	p.statusMu.Lock()
	if state == "available" && (p.healthState == "" || p.healthState == "available") {
		p.statusMu.Unlock()
		return
	}
	p.healthState = state
	sink := p.statusSink
	status := events.NewProviderStatus("openai-speech", "speech", p.model, p.base, state, message)
	p.statusMu.Unlock()
	if sink != nil {
		sink(ctx, events.Info{Message: message, ProviderStatus: status})
	}
}
