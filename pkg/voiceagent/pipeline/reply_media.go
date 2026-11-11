package pipeline

import (
	"context"
	"fmt"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func validateProviderImages(ctx context.Context, provider brain.Provider, refs []chatmedia.Ref) error {
	if len(refs) == 0 {
		return nil
	}
	validator, ok := provider.(interface {
		ValidateImages(context.Context, []chatmedia.Ref) error
	})
	if !ok {
		return fmt.Errorf("worker provider does not support image input; choose a vision-capable Ollama model or Codex")
	}
	return validator.ValidateImages(ctx, refs)
}
func finalizeProviderReply(provider brain.Provider, text, workDir string, warn func(string)) (string, []chatmedia.Ref) {
	if !strings.Contains(text, "![") {
		return text, nil
	}
	cleaned, refs, err := chatmedia.DefaultStore().ImportReply(text, workDir)
	if err != nil && warn != nil {
		warn(err.Error())
	}
	if cleaned == text && len(refs) == 0 {
		return text, nil
	}
	writer, ok := provider.(brain.ReplyMediaWriter)
	if !ok {
		if warn != nil {
			warn("This provider cannot persist image replies")
		}
		return text, nil
	}
	if err := writer.SetReplyMedia(cleaned, refs); err != nil {
		if warn != nil {
			warn(err.Error())
		}
		return text, nil
	}
	return cleaned, refs
}
func (p *Pipeline) finalizeMedia(metrics *turnMetrics, response string) events.ResponseReady {
	if metrics == nil {
		metrics = newTurnMetrics()
	}
	if metrics.mediaReply != nil {
		return *metrics.mediaReply
	}
	e := events.ResponseReady{Response: response}
	if !metrics.degraded {
		warn := func(message string) { p.emit(events.Info{Message: message}) }
		e.Response, e.Attachments = finalizeProviderReply(p.Brain, response, brain.ReplyWorkDir(p.Brain), warn)
		if t := p.transcriptFor(p.Brain); t != nil && len(e.Attachments) > 0 {
			if err := t.SetReplyMedia(e.Response, e.Attachments); err != nil {
				warn(err.Error())
			}
		}
	}
	metrics.mediaReply = &e
	return e
}
func (p *Pipeline) replyMediaEvent(metrics *turnMetrics, event events.Event) events.Event {
	e, ok := event.(events.ResponseReady)
	if !ok || e.Interrupted || e.Degraded || len(e.Attachments) > 0 {
		return event
	}
	reply := p.finalizeMedia(metrics, e.Response)
	e.Response, e.Attachments = reply.Response, reply.Attachments
	return e
}
func (a *Agency) workerReplyMedia(id string) []chatmedia.Ref {
	a.mu.Lock()
	defer a.mu.Unlock()
	turns := a.workerThreads[id]
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].Role == "assistant" || turns[i].Role == "samantha" {
			return append([]chatmedia.Ref(nil), turns[i].Attachments...)
		}
	}
	return nil
}
