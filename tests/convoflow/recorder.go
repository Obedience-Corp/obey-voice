package main

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

// recorder is the process-start event log. waitFor polls it; it never arms a
// new bus subscriber per wait.
type recorder struct {
	mu     sync.Mutex
	events []timedEvent
	routes []pipeline.RouteDecision
	spoken []string
}

type timedEvent struct {
	at    time.Time
	event events.Event
}

func (r *recorder) add(e events.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, timedEvent{at: time.Now(), event: e})
	switch v := e.(type) {
	case events.SpeechSegmentReady:
		r.addSpokenLocked(v.Text)
	case events.GeneratingVoice:
		r.addSpokenLocked(v.Sentence)
	}
}

// addSpokenLocked keeps one line per sentence: the pipeline announces the
// same text as SpeechSegmentReady and again as GeneratingVoice, and a
// doubled transcript would double-count leak lines and mislead the report.
func (r *recorder) addSpokenLocked(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	if n := len(r.spoken); n > 0 && r.spoken[n-1] == text {
		return
	}
	r.spoken = append(r.spoken, text)
}

func (r *recorder) route(d pipeline.RouteDecision) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.routes = append(r.routes, d)
}

func (r *recorder) snapshot() []timedEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]timedEvent, len(r.events))
	copy(out, r.events)
	return out
}

func (r *recorder) spokenText() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.spoken, " ")
}

func (r *recorder) lastRoute() (pipeline.RouteDecision, bool) {
	return r.lastRouteSince(0)
}

// routeMark is the number of decisions so far; lastRouteSince(mark) returns
// the newest decision made after it, so a turn only sees its own route.
func (r *recorder) routeMark() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.routes)
}

func (r *recorder) lastRouteSince(mark int) (pipeline.RouteDecision, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if mark < 0 {
		mark = 0
	}
	if len(r.routes) <= mark {
		return pipeline.RouteDecision{}, false
	}
	return r.routes[len(r.routes)-1], true
}

func (r *recorder) hasEvent(name string) bool {
	for _, te := range r.snapshot() {
		if eventName(te.event) == name {
			return true
		}
	}
	return false
}

func eventName(e events.Event) string {
	switch e.(type) {
	case events.JobQueued:
		return "job_queued"
	case events.JobStarted:
		return "job_started"
	case events.JobFinished, events.JobFailed, events.JobAborted:
		return "job_terminal"
	case events.ToolCallStarted:
		return "tool_call_started"
	case events.ResponseReady:
		return "response_ready"
	case events.SpeechSegmentReady:
		return "speech_segment_ready"
	case events.SpeakingComplete:
		return "speaking_complete"
	default:
		return ""
	}
}

func jobTerminal(e events.Event) bool {
	switch e.(type) {
	case events.JobFinished, events.JobFailed, events.JobAborted:
		return true
	default:
		return false
	}
}

// waitFor polls the existing log until the predicate is true or ctx dies.
func (r *recorder) waitFor(ctx context.Context, kind waitKind, playing func() bool) error {
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		if r.waitSatisfied(kind, playing) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

func (r *recorder) waitSatisfied(kind waitKind, playing func() bool) bool {
	idle := playing == nil || !playing()
	switch kind {
	case waitIdle:
		return idle
	case waitJobStarted:
		return idle && r.hasEvent("job_started")
	case waitJobTerminal:
		log := r.snapshot()
		termAt := -1
		for i, te := range log {
			if jobTerminal(te.event) {
				termAt = i
			}
		}
		if termAt < 0 {
			return false
		}
		for _, te := range log[termAt:] {
			switch te.event.(type) {
			case events.SpeechSegmentReady, events.SpeakingComplete, events.GeneratingVoice:
				return idle
			}
		}
		// Summary may already have been spoken before this wait; spoken log
		// after the terminal event is enough even if IsPlaying is already false.
		return idle
	default:
		return idle
	}
}

func historyContains(hist []brain.Turn, needle string) bool {
	needle = strings.ToLower(needle)
	for _, t := range hist {
		if strings.Contains(strings.ToLower(t.Content), needle) {
			return true
		}
	}
	return false
}
