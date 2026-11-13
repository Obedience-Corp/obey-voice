package personaruntime

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/pipeline"
)

// Two replies stopped one straight after the other are each kept once, in
// order, and the next request replays both on a new thread.
func TestTwoCodexRepliesStoppedInARowAreEachKeptOnce(t *testing.T) {
	c := newLocalConversation(t, 20, "ember", "mira=codex")
	c.say("Note that the vault code is cobalt.", "switch to Mira")
	c.stopAfterFirstText("First. " + claudeStall)
	c.stopAfterFirstText("Second. " + claudeStall)
	c.say("Go on.")

	for name, turns := range map[string][]brain.Turn{"transcript": c.selector.Transcript(), "saved": c.saved().Turns} {
		var order []string
		for _, turn := range turns {
			switch {
			case turn.Role == "user" && strings.Contains(turn.Content, claudeStall):
				order = append(order, turn.Content)
			case turn.Role == "assistant" && turn.PersonaID == "mira" && turn.Disposition == "interrupted" && turn.Content == claudePartial:
				order = append(order, "stopped reply")
			case turn.Role == "user" && turn.Content == "Go on.":
				order = append(order, turn.Content)
			}
		}
		want := []string{"First. " + claudeStall, "stopped reply", "Second. " + claudeStall, "stopped reply", "Go on."}
		if !slices.Equal(order, want) {
			t.Fatalf("%s holds %q, want %q: %+v", name, order, want, turns)
		}
	}
	turns := readCodexTurns(t, c.codexLog)
	last := turns[len(turns)-1]
	if last.Resumed || strings.Count(last.Prompt, "First. "+claudeStall) != 1 || strings.Count(last.Prompt, "Second. "+claudeStall) != 1 || strings.Count(last.Prompt, claudePartial) != 2 {
		t.Fatalf("the request after two stops (resumed %v) was sent:\n%s", last.Resumed, last.Prompt)
	}
}

// heldEnd is a Codex persona whose stalled request is not reported ended
// until release closes, as a CLI that ignores being stopped would be. handed
// closes once the host has been told.
type heldEnd struct {
	*brain.CodexBrain
	release, handed chan struct{}
	once            sync.Once
}

func (h *heldEnd) ThinkStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	s, err := h.CodexBrain.ThinkStream(ctx, input, opts)
	if err != nil || !strings.Contains(input, claudeStall) {
		return s, err
	}
	done := make(chan brain.StreamResult)
	go func() {
		defer close(h.handed)
		defer close(done)
		result, ok := <-s.Done
		<-h.release
		if ok {
			done <- result
		}
	}()
	return &brain.Stream{Chunks: s.Chunks, Thinking: s.Thinking, Done: done}, nil
}

func (h *heldEnd) end() { h.once.Do(func() { close(h.release) }) }

// A stopped Codex request that never ends is given up on after the stop
// wait: the conversation goes on, a switch and a clear go ahead, and when the
// request finally ends nothing of it lands in the conversation or the saved
// session.
func TestStoppedCodexRequestThatNeverEndsHoldsUpNoSwitchOrClear(t *testing.T) {
	pipeline.SetProviderStopTimeoutForTest(t, 200*time.Millisecond)
	for _, clear := range []bool{false, true} {
		t.Run(map[bool]string{false: "switch", true: "clear"}[clear], func(t *testing.T) {
			c := newLocalConversation(t, 20, "ember", "mira=codex")
			held := &heldEnd{release: make(chan struct{}), handed: make(chan struct{})}
			t.Cleanup(held.end)
			c.wrap = func(p brain.Provider) brain.Provider {
				if codex, ok := p.(*brain.CodexBrain); ok {
					held.CodexBrain = codex
					return held
				}
				return p
			}
			c.say("Note that the vault code is cobalt.", "switch to Mira")
			bounded := func(name string, run func()) {
				t.Helper()
				started := time.Now()
				run()
				if took := time.Since(started); took > 10*time.Second {
					t.Fatalf("%s took %v", name, took)
				}
			}
			bounded("the stopped turn", func() { c.stopAfterFirstText("Say more. " + claudeStall) })
			var said []string
			if clear {
				bounded("the clear", func() {
					if err := c.p.ClearConversation(); err != nil {
						t.Fatal(err)
					}
				})
				said = []string{"Go on."}
				bounded("the next line", func() { c.say(said...) })
			} else {
				said = []string{"Note that the vault code is cobalt.", "switch to Mira", "switch to Ember", "And now?", "switch to Mira", "Go on."}
				bounded("the switch", func() { c.say(said[2:]...) })
			}
			held.end()
			<-held.handed
			c.say("Anything else?")
			said = append(said, "Anything else?")

			for name, turns := range map[string][]brain.Turn{"transcript": c.selector.Transcript(), "saved": c.saved().Turns} {
				var lines []string
				for _, turn := range turns {
					if turn.Role == "user" {
						lines = append(lines, turn.Content)
					}
					if turn.Disposition != "" || strings.Contains(turn.Content, claudeStall) {
						t.Fatalf("%s holds part of the abandoned request: %+v", name, turns)
					}
				}
				if !slices.Equal(lines, said) {
					t.Fatalf("%s holds the lines %q, want %q", name, lines, said)
				}
				if replies := countTurns(turns, func(t brain.Turn) bool { return t.Role == "assistant" && t.Content != "" }); replies != len(said) {
					t.Fatalf("%s holds %d replies to %d lines: %+v", name, replies, len(said), turns)
				}
			}
		})
	}
}
