package pipeline

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/chatmedia"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// pendingDelivery is one completed job waiting to be spoken when the pipeline
// is idle. A slice, not a single string: queued jobs finish sequentially, but
// playback or a foreground turn can delay delivery, and a later completion
// must not overwrite an earlier one.
type pendingDelivery struct {
	JobID        string
	Text         string
	origin       *workerOrigin
	releaseVoice func()
	// result marks a job's answer, as opposed to a milestone line: starting
	// or giving up on its speech delivers the held reply.
	result *events.JobResultDelivered
}

// queueResult queues a finished job's answer for history and idle speech and
// returns its JobFinished. An answer that will be spoken is held: the chat
// and history take it when its speech starts, or when that speech is given up.
func (a *Agency) queueResult(id, task, result string, refs []chatmedia.Ref, elapsed time.Duration) events.JobFinished {
	a.mu.Lock()
	origin, ok := a.workerOrigins[id]
	a.mu.Unlock()
	primary, _ := a.pipeline.ttsProviders()
	if ok {
		primary = origin.Voices.primary
	}
	finishedEnvelope, deliveredEnvelope := origin.Envelope, origin.Envelope
	finished := events.JobFinished{ID: id, Task: task, Summary: result, Attachments: refs, Elapsed: elapsed, Envelope: &finishedEnvelope, Scope: origin.Scope}
	finished.SpeechPending = a.pipeline.canSpeak(primary)
	a.queueWorkerReply(id, result, finished.SpeechPending)
	var delivered *events.JobResultDelivered
	if finished.SpeechPending {
		delivered = &events.JobResultDelivered{ID: id, Task: task, Summary: result, Attachments: refs, Envelope: &deliveredEnvelope, Scope: origin.Scope}
	}
	a.enqueueSpeech(id, a.cleanSummary(result), delivered)
	return finished
}

// deliverResult releases a held answer once: the chat shows it and history
// may take it. Later calls for the same job do nothing.
func (a *Agency) deliverResult(d pendingDelivery, spoken bool) {
	if d.result == nil {
		return
	}
	a.mu.Lock()
	reply, held := a.heldReplies[d.JobID]
	if held {
		delete(a.heldReplies, d.JobID)
		if strings.TrimSpace(reply.Text) != "" {
			a.workerReplies = append(a.workerReplies, reply)
		}
	}
	a.mu.Unlock()
	if !held {
		return
	}
	event := *d.result
	event.Spoken = spoken
	a.emitJobTrace(d.JobID, event)
}

// heldResultWaiting reports a held answer that an idle flush would speak now.
func (a *Agency) heldResultWaiting() bool {
	if a == nil || a.workEnabled() {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.canSpeakIdleSummary() && slices.ContainsFunc(a.pending, func(d pendingDelivery) bool { return d.result != nil })
}

func (a *Agency) enqueueSummary(id, text string) { a.enqueueSpeech(id, text, nil) }

func (a *Agency) enqueueSpeech(id, text string, result *events.JobResultDelivered) {
	text = strings.TrimSpace(text)
	if text == "" {
		text = "That finished with nothing to report."
	}
	a.mu.Lock()
	next := pendingDelivery{JobID: id, Text: text, result: result}
	if origin, ok := a.workerOrigins[id]; ok {
		next.origin = &origin
		next.releaseVoice = a.pipeline.RetainTTS(origin.Voices.primary, origin.Voices.fallback)
	}
	a.pending = append(a.pending, next)
	a.mu.Unlock()
}

func (a *Agency) cleanSummary(raw string) string {
	cleaned := strings.TrimSpace(brain.CleanForVoice(raw))
	if speakableSegment(cleaned) {
		return cleaned
	}
	if strings.TrimSpace(raw) == "" {
		return "That finished with nothing to report."
	}
	return cleaned
}

func (a *Agency) tryFlushPending() {
	if a.workEnabled() {
		return
	}
	for {
		a.mu.Lock()
		if len(a.pending) == 0 {
			a.mu.Unlock()
			return
		}
		if !a.canSpeakIdleSummary() {
			a.mu.Unlock()
			return
		}
		next := a.pending[0]
		a.pending = a.pending[1:]
		a.mu.Unlock()
		ctx := context.Background()
		var metrics *turnMetrics
		if origin := next.origin; origin != nil {
			if origin.Scope.Valid() && (origin.Scope != a.pipeline.ConversationScope() || a.pipeline.ConversationRetired()) {
				next.releaseVoice()
				a.deliverResult(next, false)
				continue
			}
			ctx = context.WithValue(events.WithResponse(ctx, origin.Envelope), responseVoicesKey{}, origin.Voices)
			metrics = newTurnMetrics()
			metrics.response = origin.Envelope
			metrics.ttsEngine = ttsIdentityFor(origin.Voices.primary)
		}
		if next.result != nil {
			if metrics == nil {
				metrics = newTurnMetrics()
			}
			metrics.onAudible = func() { a.deliverResult(next, true) }
		}
		err := a.pipeline.speakStandalone(ctx, nil, metrics, next.Text)
		if next.releaseVoice != nil {
			next.releaseVoice()
		}
		a.deliverResult(next, false)
		if err != nil {
			a.pipeline.emit(events.Info{Message: fmt.Sprintf("job %s summary speech failed: %v", next.JobID, err)})
		}
	}
}

func (a *Agency) canSpeakIdleSummary() bool {
	if a.pipeline.NarrationActive != nil && a.pipeline.NarrationActive() {
		return false
	}
	if a.pipeline.Player != nil && a.pipeline.Player.IsPlaying() {
		return false
	}
	if a.pipeline.activeTurns.Load() > 0 {
		return false
	}
	return true
}

// activeTurns counts in-flight RunTurn / RunTurnTextMode calls for idle summary gating.
func (p *Pipeline) incActiveTurn() {
	if p == nil {
		return
	}
	p.activeTurns.Add(1)
}

func (p *Pipeline) decActiveTurn() {
	if p == nil {
		return
	}
	p.activeTurns.Add(-1)
	if p.Agency != nil {
		p.Agency.AfterTurn()
	}
}
