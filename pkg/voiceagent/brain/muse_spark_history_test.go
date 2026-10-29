package brain

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Obedience-Corp/muse-spark-go/pkg/muse"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/continuity"
)

func TestMuseSparkRetainsTranscriptBeyondPromptLimit(t *testing.T) {
	f := &fakeMuse{result: &muse.Result{Terminal: "completed", Text: "answer"}, events: []muse.Event{museEvent("run.terminal.completed", "answer")}}
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			b := testMuse(f)
			b.cfg.MaxHistory = 2
			for i := range 3 {
				input := fmt.Sprintf("request-%d", i)
				var err error
				if streaming {
					var s *Stream
					s, err = b.ThinkStream(t.Context(), input, StreamOptions{})
					if err == nil {
						for range s.Chunks {
						}
						err = (<-s.Done).Err
					}
				} else {
					_, err = b.ThinkFull(t.Context(), input, StreamOptions{})
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if got := len(b.History()); got != 6 {
				t.Fatalf("durable transcript has %d turns after three exchanges, want 6", got)
			}
			if got := len(b.ContextHistory()); got != 4 {
				t.Fatalf("prompt has %d turns, want two whole exchanges", got)
			}
			prompt := b.buildPrompt("next", StreamOptions{})
			if strings.Contains(prompt, "request-0") || !strings.Contains(prompt, "request-1") {
				t.Fatal(prompt)
			}
			if b.SessionInfo().Turns != 6 || !reflect.DeepEqual(b.History(), b.CanonicalHistory()) {
				t.Fatal("reporting lost transcript")
			}
		})
	}
}

func TestMuseSparkReseedAndTransferPreserveCanonical(t *testing.T) {
	b := testMuse(&fakeMuse{})
	b.cfg.MaxHistory = 1
	original := []Turn{{Role: "user", Content: "old request"}, {Role: "assistant", Content: "old answer", PersonaID: "jarvis", ResponseID: "r1"},
		{Role: "user", Content: "recent request"}, {Role: "assistant", ToolCalls: []TurnToolCall{{ID: "c1", Name: "lookup"}}}, {Role: "tool", Content: "tool evidence", ToolCallID: "c1"}, {Role: "assistant", Content: "recent answer", PersonaID: "obey", ResponseID: "r2"}}
	b.LoadHistory(original)
	seed := append([]Turn{{Role: "assistant", Content: "shared summary"}}, original[2:]...)
	b.ReseedContinuity(seed)
	if !reflect.DeepEqual(b.History(), original) {
		t.Fatal("reseed rewrote canonical")
	}
	if !reflect.DeepEqual(b.ContextHistory(), seed) {
		t.Fatal("reseed dropped summary or split tool exchange")
	}
	b.appendTurn(Turn{Role: "user", Content: "after summary"})
	b.appendTurn(Turn{Role: "assistant", Content: "continued", PersonaID: "obey"})
	if got := b.ContextHistory(); len(got) != 3 || got[0].Content != "shared summary" || got[1].Content != "after summary" {
		t.Fatalf("trim lost compaction: %+v", got)
	}
	snapshot := b.ContinuitySnapshot()
	snapshot.Archive = []continuity.Entry{{ID: "archive", Summary: "tool provenance"}}
	snapshot.ContextHighWater = 8192
	foreign := &CodexBrain{cfg: b.cfg.Clone()}
	if err := foreign.ImportContinuity(snapshot); err != nil {
		t.Fatal(err)
	}
	next := testMuse(&fakeMuse{})
	if err := next.ImportContinuity(foreign.ContinuitySnapshot()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snapshot, next.ContinuitySnapshot()) {
		t.Fatal("transfer expanded working view or lost metadata")
	}
	snapshot.Canonical[0].Content = "mutated"
	snapshot.Canonical[3].ToolCalls[0].Name = "mutated"
	snapshot.Working[0].Content = "mutated"
	snapshot.Archive[0].Summary = "mutated"
	got := next.ContinuitySnapshot()
	if got.Canonical[0].Content == "mutated" || got.Canonical[3].ToolCalls[0].Name == "mutated" || got.Working[0].Content == "mutated" || got.Archive[0].Summary == "mutated" {
		t.Fatal("transfer aliases input")
	}
	returned := next.History()
	returned[3].ToolCalls[0].Name = "returned mutation"
	if next.History()[3].ToolCalls[0].Name == "returned mutation" {
		t.Fatal("history aliases nested calls")
	}
	fresh := next.Fresh().(*MuseSparkBrain)
	if len(fresh.History()) != 0 || len(fresh.ContextHistory()) != 0 || len(fresh.ContinuitySnapshot().Archive) != 0 {
		t.Fatal("fresh inherited session")
	}
	next.ClearHistory()
	if s := next.ContinuitySnapshot(); len(s.Canonical) != 0 || len(s.Working) != 0 || len(s.Archive) != 0 || s.ContextHighWater != 0 {
		t.Fatal("clear retained state")
	}
}

func TestMuseSparkContinuityEmptyViewAndRestore(t *testing.T) {
	b := testMuse(&fakeMuse{})
	original := []Turn{{Role: "user", Content: "retained transcript"}, {Role: "assistant", Content: "answer"}}
	for _, empty := range []bool{true, false} {
		s := ContinuitySnapshot{Canonical: original}
		if empty {
			s.Working = []Turn{}
		}
		if err := b.ImportContinuity(s); err != nil {
			t.Fatal(err)
		}
		got := b.ContinuitySnapshot()
		if empty && (got.Working == nil || len(got.Working) != 0) {
			t.Fatal("empty projection expanded")
		}
		if !empty && !reflect.DeepEqual(got.Working, original) {
			t.Fatal("legacy snapshot did not seed transcript")
		}
	}
	b.ReseedContinuity([]Turn{{Role: "assistant", Content: "summary"}})
	if err := b.AppendAssistantResponseWithIdentity("host output", TurnIdentity{ResponseID: "host-r"}); err != nil {
		t.Fatal(err)
	}
	if b.History()[2].ResponseID != "host-r" || b.ContextHistory()[1].ResponseID != "host-r" {
		t.Fatal("host output missing from canonical or view")
	}
	b.LoadHistory(original)
	if !reflect.DeepEqual(b.ContextHistory(), original) || !reflect.DeepEqual(b.History(), original) {
		t.Fatal("load did not restore independent conversation")
	}
}

func TestMuseSparkTransientAndFailedSummaryLeaveBothHistories(t *testing.T) {
	f := &fakeMuse{result: &muse.Result{Terminal: "completed", Text: "summary"}}
	b := testMuse(f)
	b.LoadHistory([]Turn{{Role: "user", Content: "original"}, {Role: "assistant", Content: "answer"}})
	b.ReseedContinuity([]Turn{{Role: "assistant", Content: "prior summary"}})
	before := b.ContinuitySnapshot()
	if _, err := b.ThinkFull(context.Background(), "summarize", StreamOptions{TransientHistory: true}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, b.ContinuitySnapshot()) {
		t.Fatal("transient summary changed history")
	}
	f.result.Terminal = "failed"
	if _, err := b.ThinkFull(context.Background(), "summarize", StreamOptions{TransientHistory: true}); err == nil {
		t.Fatal("failed summary succeeded")
	}
	if !reflect.DeepEqual(before, b.ContinuitySnapshot()) {
		t.Fatal("failed summary changed history")
	}
}

func TestMuseSparkReplayRetainsSharedAndToolRoles(t *testing.T) {
	b := testMuse(&fakeMuse{})
	b.LoadHistory([]Turn{{Role: "user", Content: "lookup", Speaker: "speaker-2"}, {Role: "assistant", PersonaID: "jarvis", ToolCalls: []TurnToolCall{{ID: "c1", Name: "lookup"}}}, {Role: "tool", ToolName: "lookup", ToolCallID: "c1", Content: "tool evidence"}})
	b.ReseedContinuity(append([]Turn{{Role: "assistant", Content: "shared summary"}}, b.ContextHistory()...))
	prompt := b.buildPrompt("next", StreamOptions{})
	for _, want := range []string{`"role":"assistant","content":"shared summary"`, `"persona":"jarvis"`, `"role":"tool","content":"tool evidence"`, `"tool_call_id":"c1"`, `"speaker":"speaker-2"`} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("missing %s in %s", want, prompt)
		}
	}
	if strings.Contains(prompt, "Obey: shared summary") || strings.Contains(prompt, "User: tool evidence") {
		t.Fatal("replay fabricated historical authorship")
	}
}
