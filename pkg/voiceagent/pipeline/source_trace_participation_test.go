package pipeline

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/discussion"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

func TestTraceMergedParticipationSkipRemainsYielded(t *testing.T) {
	for _, voice := range []bool{false, true} {
		t.Run(fmt.Sprint("voice=", voice), func(t *testing.T) {
			bus := events.NewBus()
			b := &continuationBrain{chunks: []string{"must not generate"}}
			p := &Pipeline{Events: bus, Brain: b, STT: &fakeSTT{text: "hello"}, GroupParticipationEnabled: true}
			if err := p.BeginConversationIdentity("skip"); err != nil {
				t.Fatal(err)
			}
			p.AdmitInput = func(context.Context, string, bool) (ResponseAdmission, error) {
				return ResponseAdmission{Skip: true}, nil
			}
			var mu sync.Mutex
			var spans []events.PipelineTrace
			events.Subscribe(bus, func(e events.PipelineTrace) { mu.Lock(); spans = append(spans, e); mu.Unlock() })
			var err error
			if voice {
				_, err = p.RunTurn(t.Context())
			} else {
				err = p.RunTurnTextMode(t.Context(), "hello")
			}
			if err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			admitted, yielded := false, false
			for _, e := range spans {
				if e.Span.State != "end" {
					continue
				}
				switch e.Span.Stage {
				case "admission":
					admitted = e.Span.Status == "completed"
				case "turn":
					yielded = e.Span.Status == "skipped"
					if e.Trace == nil || e.Trace.TurnID == nil {
						t.Fatal("yielded invocation lost source identity")
					}
				case "model", "synthesis", "playback":
					t.Fatalf("yielded turn fabricated %s", e.Span.Stage)
				}
			}
			if !admitted || !yielded {
				t.Fatalf("admission completed=%v, turn skipped=%v", admitted, yielded)
			}
			if inputs, _ := b.calls(); len(inputs) != 0 {
				t.Fatal("yielded admission invoked brain")
			}
		})
	}
}

func TestTraceMergedDiscussionContinuationKeepsParentAndOwner(t *testing.T) {
	bus := events.NewBus()
	b := &continuationBrain{chunks: []string{"A useful contribution."}}
	p := &Pipeline{Events: bus, Brain: b, GroupParticipationEnabled: true}
	if err := p.BeginConversationIdentity("discussion"); err != nil {
		t.Fatal(err)
	}
	state := discussion.New(t.Context(), "hello", "all", "invited", []string{"same", "other"})
	directive := &discussion.Directive{ID: state.Snapshot().ID, State: state, Original: "hello"}
	next := 0
	admit := func() ResponseAdmission {
		next++
		return ResponseAdmission{Request: "hello", Discussion: directive, Envelope: events.ResponseEnvelope{PersonaID: "same", ResponseID: fmt.Sprintf("response-%d", next)}}
	}
	p.AdmitInput = func(context.Context, string, bool) (ResponseAdmission, error) { return admit(), nil }
	handoffs := 0
	p.RouteHandoff = func(ctx context.Context, _ brain.Turn, _ []string) (HandoffProposal, bool, error) {
		completion := completionFromContext(ctx)
		if completion == nil || completion.owner == nil || completion.discussion != directive || completion.trace.TurnID == nil {
			t.Error("merged owner/discussion/frozen trace state lost")
		}
		handoffs++
		if handoffs > 2 {
			return HandoffProposal{}, false, nil
		}
		return HandoffProposal{DiscussionID: directive.ID, PersonaID: "same", Commit: func(context.Context) (ResponseAdmission, error) { return admit(), nil }}, true, nil
	}
	var mu sync.Mutex
	var ends []events.PipelineTrace
	events.Subscribe(bus, func(e events.PipelineTrace) {
		if e.Span.Stage == "turn" && e.Span.State == "end" {
			mu.Lock()
			ends = append(ends, e)
			mu.Unlock()
		}
	})
	if err := p.RunTurnTextMode(t.Context(), "hello"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(ends) != 3 {
		t.Fatalf("discussion responses=%d", len(ends))
	}
	turns := map[string]bool{}
	responses := map[string]bool{}
	for i, e := range ends {
		tr := e.Trace
		if tr == nil || tr.TurnID == nil || tr.ResponseID == nil || tr.PersonaID == nil || *tr.PersonaID != "same" || tr.RuntimeID == "" || tr.SessionID != "discussion" || tr.ConversationEpoch == "" || e.Span.Status != "completed" {
			t.Fatalf("response%d: %+v", i, e)
		}
		if turns[*tr.TurnID] || responses[*tr.ResponseID] {
			t.Fatal("repeated participant reused invocation/response identity")
		}
		turns[*tr.TurnID] = true
		responses[*tr.ResponseID] = true
		if i == 0 {
			if tr.ParentTurnID != nil {
				t.Fatal("root turn acquired parent")
			}
		} else if tr.ParentTurnID == nil || *tr.ParentTurnID != *ends[i-1].Trace.TurnID {
			t.Fatal("continuation causal parent lost")
		}
	}
}
