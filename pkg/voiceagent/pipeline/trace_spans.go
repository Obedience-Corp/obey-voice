package pipeline

import (
	"context"
	"fmt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"time"
)

type segmentTraceKey struct{}

func withSegmentTrace(ctx context.Context, index int) context.Context {
	return context.WithValue(ctx, segmentTraceKey{}, index)
}
func segmentTraceIndex(ctx context.Context) *int {
	i, ok := ctx.Value(segmentTraceKey{}).(int)
	if !ok {
		return nil
	}
	return &i
}
func traceOffset(trace events.TraceContext, at time.Time) *float64 {
	if at.IsZero() || trace.Origin.IsZero() {
		return nil
	}
	ms := float64(at.Sub(trace.Origin)) / float64(time.Millisecond)
	return &ms
}
func (p *Pipeline) traceSpan(trace events.TraceContext, id, stage, state, status string, start, end time.Time, index *int, attempt *string) {
	if trace.RuntimeID == "" {
		return
	}
	switch status {
	case "succeeded", "done":
		status = "completed"
	case "canceled", "aborted":
		status = "cancelled"
	case "yielded":
		status = "skipped"
	}
	trace = p.stampTrace(trace)
	parent := trace.TurnID
	if stage == "turn" {
		parent = nil
	}
	p.emit(events.PipelineTrace{Trace: &trace, Span: events.TraceSpan{ID: id, ParentID: parent, Stage: stage, Lane: stage, State: state, Status: status, StartMS: traceOffset(trace, start), EndMS: traceOffset(trace, end), SegmentIndex: index, AttemptID: attempt}})
}
func traceID(trace events.TraceContext, stage string) string {
	if trace.TurnID != nil {
		return *trace.TurnID + ":" + stage
	}
	return newWorkID()
}
func (p *Pipeline) startTrace(ctx context.Context, stage string) func(string) {
	finish := p.startTraceBoundary(ctx, stage)
	return func(status string) { finish(status, time.Now()) }
}

// startTraceBoundary allows an operation's measured terminal boundary to remain
// distinct from later cleanup or delivery of the observation.
func (p *Pipeline) startTraceBoundary(ctx context.Context, stage string) func(string, time.Time) {
	trace := events.TraceFromContext(ctx)
	id := traceID(trace, stage)
	start := time.Now()
	p.traceSpan(trace, id, stage, "start", "running", start, time.Time{}, nil, nil)
	return func(status string, end time.Time) { p.traceSpan(trace, id, stage, "end", status, start, end, nil, nil) }
}
func (p *Pipeline) tracePlayback(metrics *turnMetrics, event playbackEvent) {
	trace := metrics.trace
	index := event.index
	if event.attemptID != "" && !(event.kind == playbackNotEnqueued && event.synthEnded.IsZero()) {
		status := "running"
		state := "update"
		if event.kind != playbackStarted {
			state = "end"
			status = "completed"
			if event.synthError != "" {
				status = "failed"
			}
			if event.synthEnded.IsZero() {
				status = "unknown"
				if event.result.Interrupted {
					status = "cancelled"
				}
			}
		}
		p.traceSpan(trace, event.attemptID, "synthesis", state, status, event.synthStarted, event.synthEnded, &index, &event.attemptID)
		if !event.synthFirstPCM.IsZero() {
			p.traceSpan(trace, event.attemptID+":pcm", "first_pcm", "update", "completed", event.synthFirstPCM, event.synthFirstPCM, &index, &event.attemptID)
		}
	}
	status := "running"
	state := "start"
	if event.kind != playbackStarted {
		state = "end"
		status = "completed"
		if event.error != "" {
			status = "failed"
		}
		if event.result.Interrupted {
			status = "interrupted"
		}
		if event.playStarted.IsZero() {
			status = "skipped"
		}
	}
	if event.traceStatus != "" {
		status = event.traceStatus
	}
	p.traceSpan(trace, traceID(trace, "playback")+":"+fmt.Sprint(index), "playback", state, status, event.playStarted, event.playEnded, &index, nil)
}

func (a *Agency) traceJobBoundary(id, state string) {
	a.mu.Lock()
	origin := a.workerOrigins[id]
	a.mu.Unlock()
	trace := a.jobTrace(id)
	if trace.RuntimeID == "" {
		return
	}
	prefix := trace.RuntimeID + ":job:" + id
	started, ended, _ := a.jobTiming(id)
	switch state {
	case "queued":
		a.pipeline.traceSpan(trace, prefix+":queue", "worker_queue", "start", "running", origin.traceQueuedAt, time.Time{}, nil, nil)
	case "started":
		a.pipeline.traceSpan(trace, prefix+":queue", "worker_queue", "end", "completed", origin.traceQueuedAt, started, nil, nil)
		a.pipeline.traceSpan(trace, prefix, "worker", "start", "running", started, time.Time{}, nil, nil)
	default:
		if started.IsZero() {
			a.pipeline.traceSpan(trace, prefix+":queue", "worker_queue", "end", state, origin.traceQueuedAt, ended, nil, nil)
		}
		a.pipeline.traceSpan(trace, prefix, "worker", "end", state, started, ended, nil, nil)
	}
}
