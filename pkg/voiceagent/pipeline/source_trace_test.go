package pipeline

import (
	"context"
	"errors"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
	"sync"
	"testing"
	"time"
)

func TestTraceInvocationIdentityIsFrozen(t *testing.T) {
	p := &Pipeline{Events: events.NewBus()}
	if err := p.BeginConversationIdentity("session"); err != nil {
		t.Fatal(err)
	}
	a := newTurnMetrics()
	p.newTurnConductor(a)
	a.response = events.ResponseEnvelope{ResponseID: "old", PersonaID: "persona-a"}
	a.trace = responseTrace(a.trace, a.response)
	ctx := events.WithResponse(events.WithTrace(context.Background(), a.trace), a.response)
	b := newTurnMetrics()
	p.newTurnConductor(b, ctx)
	if b.trace.ParentTurnID == nil || *b.trace.ParentTurnID != *a.trace.TurnID || *a.trace.TurnID == *b.trace.TurnID {
		t.Fatal("continuation invocation identity lost")
	}
	if b.trace.RuntimeID != a.trace.RuntimeID || b.trace.TurnStartMS == nil {
		t.Fatal("source clock domain lost")
	}
	var got events.ResponseReady
	events.Subscribe(p.Events, func(e events.ResponseReady) { got = e })
	p.emitResponseContext(ctx, events.ResponseReady{Response: "delayed"})
	if got.Trace == nil || got.Trace.ResponseID == nil || *got.Trace.ResponseID != "old" || *got.Trace.TurnID != *a.trace.TurnID {
		t.Fatal("delayed event lost immutable source identity")
	}
	p.emit(events.ResponseReady{Response: "unscoped"})
	if got.Trace == nil || got.Trace.TurnID != nil || got.Trace.ResponseID != nil {
		t.Fatal("unscoped event borrowed current response")
	}
	if err := p.BeginConversationIdentity("new-session"); err != nil {
		t.Fatal(err)
	}
	p.emitResponseContext(ctx, events.ResponseReady{Response: "late old runtime"})
	if got.Trace.RuntimeID != a.trace.RuntimeID {
		t.Fatal("late event was relabeled after reset")
	}
}

func TestTraceOperationOutcomesAndParent(t *testing.T) {
	p := &Pipeline{Events: events.NewBus()}
	if err := p.BeginConversationIdentity("session"); err != nil {
		t.Fatal(err)
	}
	var spans []events.PipelineTrace
	events.Subscribe(p.Events, func(e events.PipelineTrace) { spans = append(spans, e) })
	metrics := newTurnMetrics()
	metrics.response = events.ResponseEnvelope{ResponseID: "canonical", PersonaID: "frozen"}
	turn := p.newTurnConductor(metrics)
	if metrics.trace.ResponseID == nil || *metrics.trace.ResponseID != "canonical" {
		t.Fatal("preassigned canonical response lost")
	}
	metrics.modelRequest = metrics.start
	metrics.modelComplete = metrics.start.Add(time.Millisecond)
	p.tracePlayback(metrics, playbackEvent{kind: playbackFinished, attemptID: "attempt", synthStarted: metrics.start, synthEnded: metrics.start.Add(time.Millisecond), playStarted: metrics.start, playEnded: metrics.start.Add(2 * time.Millisecond), result: audio.PlaybackResult{Interrupted: true}})
	turn.finish(TurnInterrupted)
	turn.finish(TurnFailed)
	turnEnds := 0
	for _, e := range spans {
		if e.Span.Stage == "turn" {
			if e.Span.ParentID != nil {
				t.Fatal("turn self-parent cycle")
			}
			if e.Span.State == "end" {
				turnEnds++
			}
		}
		if e.Span.Stage == "model" && e.Span.State == "end" && e.Span.Status != "completed" {
			t.Fatal("playback interruption relabeled completed model")
		}
		if e.Span.Stage == "synthesis" && e.Span.State == "end" && e.Span.Status != "completed" {
			t.Fatal("playback interruption relabeled completed synthesis")
		}
	}
	if turnEnds != 1 {
		t.Fatalf("terminal turn spans=%d", turnEnds)
	}
}

func TestTraceWorkEnabledInitialScope(t *testing.T) {
	jobs := job.NewTable(context.Background())
	t.Cleanup(jobs.Close)
	p := &Pipeline{Events: events.NewBus(), Brain: &lifecycleWorker{reply: "hello"}}
	p.Agency = NewAgency(p, jobs, &lifecycleWorker{reply: "done"}, &config.Config{}, nil)
	var got events.WorkStateChanged
	events.Subscribe(p.Events, func(e events.WorkStateChanged) { got = e })
	if err := p.EnableConversationWork("work-session", nil, &config.Config{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.CloseWork() })
	if got.Trace == nil || got.Trace.RuntimeID != got.Snapshot.Scope.RuntimeEpoch || got.Trace.MonoMS == nil || got.Trace.SessionID != "work-session" {
		t.Fatalf("initial work event has wrong clock: %+v", got.Trace)
	}
}

func TestTracePipelineEmitSites(t *testing.T) {
	for _, mode := range []string{"normal", "rejected", "fallback", "interrupted"} {
		t.Run(mode, func(t *testing.T) {
			bus := events.NewBus()
			player := newFakePlayer(10 * time.Millisecond)
			defer player.Close()
			p := &Pipeline{STT: &fakeSTT{text: "hello"}, Brain: &fakeBrain{chunks: []string{"This is a controlled spoken answer."}}, TTS: &fakeTTS{delay: time.Millisecond}, Player: player, Events: bus}
			if err := p.BeginConversationIdentity("trace-" + mode); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			p.AdmitInput = func(ctx context.Context, input string, background bool) (ResponseAdmission, error) {
				if mode == "rejected" {
					return ResponseAdmission{}, errors.New("controlled rejection")
				}
				return ResponseAdmission{Request: input, Envelope: events.ResponseEnvelope{ResponseID: "response-" + mode, PersonaID: "frozen-persona"}}, nil
			}
			if mode == "fallback" {
				p.TTS = &errTTS{err: &tts.ProviderError{Provider: "primary", Kind: tts.ProviderErrorWorker, Err: errors.New("controlled primary failure")}}
				p.TTSFallback = &fakeTTS{delay: time.Millisecond}
			}
			if mode == "interrupted" {
				events.Subscribe(bus, func(e events.SpeakingStarted) { cancel(); player.Stop() })
			}
			var mu sync.Mutex
			var spans []events.PipelineTrace
			events.Subscribe(bus, func(e events.PipelineTrace) { mu.Lock(); spans = append(spans, e); mu.Unlock() })
			_, _ = p.RunTurn(ctx)
			mu.Lock()
			defer mu.Unlock()
			stages := map[string]bool{}
			attempts := map[string]string{}
			turnEnds := 0
			for _, e := range spans {
				if e.Trace == nil || e.Trace.RuntimeID == "" || e.Trace.TurnID == nil || e.Trace.MonoMS == nil || e.Trace.TurnStartMS == nil {
					t.Fatalf("source identity missing on %s: %+v", e.Span.Stage, e.Trace)
				}
				if e.Span.StartMS != nil && e.Span.EndMS != nil && *e.Span.EndMS < *e.Span.StartMS {
					t.Fatalf("inverted source bounds: %+v", e.Span)
				}
				stages[e.Span.Stage] = true
				if e.Span.Stage == "turn" && e.Span.State == "end" {
					turnEnds++
					if mode == "interrupted" && e.Span.Status != "interrupted" {
						t.Fatalf("interrupted turn outcome=%s", e.Span.Status)
					}
				}
				if e.Span.Stage == "synthesis" && e.Span.AttemptID != nil {
					if e.Trace.ResponseID == nil || *e.Trace.ResponseID != "response-"+mode {
						t.Fatal("synthesis lost frozen response")
					}
					if e.Span.State == "end" {
						attempts[*e.Span.AttemptID] = e.Span.Status
					}
				}
			}
			if turnEnds != 1 || !stages["stt"] || !stages["admission"] {
				t.Fatalf("source boundaries missing: ends=%d stages=%v", turnEnds, stages)
			}
			if mode == "rejected" {
				if stages["synthesis"] {
					t.Fatal("rejected input synthesized")
				}
				return
			}
			if !stages["model"] || !stages["segment_queue"] || !stages["synthesis"] || !stages["playback"] {
				t.Fatalf("source emit sites missing: %v", stages)
			}
			if mode == "fallback" {
				failed, completed := false, false
				for _, status := range attempts {
					failed = failed || status == "failed"
					completed = completed || status == "completed"
				}
				if len(attempts) != 2 || !failed || !completed {
					t.Fatalf("fallback attempts collapsed: %v", attempts)
				}
			}
		})
	}
}
