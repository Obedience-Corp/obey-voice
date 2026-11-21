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
		return fmt.Errorf("worker provider does not support image input; choose a vision-capable Ollama model, Codex or Claude")
	}
	return validator.ValidateImages(ctx, refs)
}
func finalizeProviderReply(provider brain.Provider, text, workDir string, warn func(string)) (string, []chatmedia.Ref, error) {
	if !strings.Contains(text, "![") {
		return text, nil, nil
	}
	cleaned, refs, err := chatmedia.DefaultStore().ImportReply(text, workDir)
	if err != nil && warn != nil {
		warn(err.Error())
	}
	if cleaned == text && len(refs) == 0 {
		return text, nil, nil
	}
	writer, ok := provider.(brain.ReplyMediaWriter)
	if !ok {
		if warn != nil {
			warn("This provider cannot persist image replies")
		}
		return text, nil, nil
	}
	if err := writer.SetReplyMedia(cleaned, refs); err != nil {
		if warn != nil {
			warn(err.Error())
		}
		return text, nil, nil
	}
	return cleaned, refs, err
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
		e.Response, e.Attachments, _ = finalizeProviderReply(p.Brain, response, brain.ReplyWorkDir(p.Brain), warn)
		if t := p.transcriptFor(p.Brain); t != nil && len(e.Attachments) > 0 {
			if reply := metrics.recording.recordedReply(); reply == nil {
				warn("This reply's images were not saved: the reply was not recorded")
			} else if err := t.SetRecordedReplyMedia(*reply, e.Response, e.Attachments); err != nil {
				warn(err.Error())
			}
		}
	}
	metrics.mediaReply = &e
	return e
}

// publishReplyMedia imports once. The terminal reply reuses the same stored assets.
func (p *Pipeline) publishReplyMedia(metrics *turnMetrics, response string) string {
	reply := p.finalizeMedia(metrics, response)
	if len(reply.Attachments) > 0 {
		p.emitResponse(metrics, events.ReplyImagesReady{Response: reply.Response, Attachments: reply.Attachments})
	}
	return reply.Response
}

func (p *Pipeline) publishStreamImages(metrics *turnMetrics, raw string, repair *promiseRepairState) {
	if _, _, routed := repair.routedKind(); routed {
		return
	}
	reply := imageReplyText(raw)
	if reply == "" {
		return
	}
	// A tool promise may still route to a worker as queued sentences reach
	// the speech gate. Let the routed turn own its eventual chat response.
	if repair.enabled && !repair.toolsFired && isToolPromise(strings.Join(strings.Fields(reply), " ")) {
		return
	}
	p.publishReplyMedia(metrics, reply)
}

// imageReplyText preserves standalone image directives that speech segmentation joins.
func imageReplyText(raw string) string {
	if strings.Contains(raw, "![") {
		cleaned := brain.CleanForVoice(raw)
		if chatmedia.ReplyProse(cleaned) != cleaned {
			return cleaned
		}
	}
	return ""
}

func (p *Pipeline) replyMediaEvent(metrics *turnMetrics, event events.Event) events.Event {
	e, ok := event.(events.ResponseReady)
	if !ok || len(e.Attachments) > 0 {
		return event
	}
	if metrics != nil && metrics.mediaReply != nil {
		e.Response, e.Attachments = metrics.mediaReply.Response, metrics.mediaReply.Attachments
		return e
	}
	if e.Degraded || (e.Interrupted && (metrics == nil || metrics.modelOutcome != "completed")) {
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
