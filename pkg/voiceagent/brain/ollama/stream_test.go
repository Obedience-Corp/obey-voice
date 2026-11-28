package ollama

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
)

type passThrough struct{}

func (passThrough) Feed(text string) string { return text }
func (passThrough) Flush() string           { return "" }

type scriptedRound struct {
	chunks []string
	tools  bool
}

func (scripted scriptedRound) play(t *testing.T, stream *Stream) {
	t.Helper()
	round := stream.NewRound(context.Background(), RoundOptions{Labels: passThrough{}, Markers: passThrough{}})
	for _, chunk := range scripted.chunks {
		if err := round.receive(api.Message{Content: chunk}); err != nil {
			t.Fatalf("receive(%q): %v", chunk, err)
		}
	}
	if err := round.FinishSpeech(round.FlushFilters(nil)); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if scripted.tools {
		round.EndThought()
	}
}

func says(chunks ...string) scriptedRound      { return scriptedRound{chunks: chunks} }
func thenCalls(chunks ...string) scriptedRound { return scriptedRound{chunks: chunks, tools: true} }

// Models differ in the white space they leave around a tool call: none, a
// space, one newline, a blank line, on either side or both, in the same chunk
// as the words or on its own. Whatever they leave, the listener gets the two
// thoughts one blank line apart.
func TestThoughtsAroundAToolCallAreOneBlankLineApart(t *testing.T) {
	const apart = "Let me look.\n\nHere it is."
	for name, tc := range map[string]struct {
		rounds []scriptedRound
		want   string
	}{
		"nothing at either edge":           {[]scriptedRound{thenCalls("Let me look."), says("Here it is.")}, apart},
		"later round opens on a space":     {[]scriptedRound{thenCalls("Let me look."), says(" Here it is.")}, apart},
		"later round opens on blank lines": {[]scriptedRound{thenCalls("Let me look."), says("\n\nHere it is.")}, apart},
		"blank lines sent on their own":    {[]scriptedRound{thenCalls("Let me look."), says("\n", "\n", "Here", " it is.")}, apart},
		"earlier round ends on a newline":  {[]scriptedRound{thenCalls("Let me look.\n"), says("Here it is.")}, apart},
		"earlier round ends on blank line": {[]scriptedRound{thenCalls("Let me look.\n\n"), says("Here it is.")}, apart},
		"blank lines on both sides":        {[]scriptedRound{thenCalls("Let me look.\n\n"), says("\n\nHere it is.")}, apart},
		"a round of white space between":   {[]scriptedRound{thenCalls("Let me look."), thenCalls("\n"), says("Here it is.")}, apart},
		"a silent round between":           {[]scriptedRound{thenCalls("Let me look."), thenCalls(), says("Here it is.")}, apart},
		"white space inside a round stays": {[]scriptedRound{thenCalls("Let me look."), says("Here", "\n\n\n", "it is.")}, "Let me look.\n\nHere\n\n\nit is."},
		"one round is left as it was sent": {[]scriptedRound{says(" One.\n\n\nTwo.\n")}, " One.\n\n\nTwo.\n"},
		"last round is only white space":   {[]scriptedRound{thenCalls("Let me look."), says("\n")}, "Let me look.\n\n"},
		"nothing follows the tool call":    {[]scriptedRound{thenCalls("Let me look."), says()}, "Let me look."},
	} {
		t.Run(name, func(t *testing.T) {
			var heard, model strings.Builder
			stream := &Stream{
				Speak: func(text string) error { heard.WriteString(text); return nil },
				Think: func(string) error { return nil },
			}
			for _, scripted := range tc.rounds {
				model.WriteString(strings.Join(scripted.chunks, ""))
				scripted.play(t, stream)
			}
			if heard.String() != tc.want {
				t.Errorf("listener got %q, want %q", heard.String(), tc.want)
			}
			// What the rounds are recorded as having said is the model's own
			// text; the restatement and repetition checks compare against it.
			if stream.spoken.String() != model.String() {
				t.Errorf("recorded %q, want the model's text %q", stream.spoken.String(), model.String())
			}
		})
	}
}

// An answer that was cut off is continued behind a break the caller sends at
// once. When a break is also owed after a tool call, the two do not add up.
func TestAContinuationBreakCountsTowardTheBlankLine(t *testing.T) {
	var heard strings.Builder
	stream := &Stream{
		Speak: func(text string) error { heard.WriteString(text); return nil },
		Think: func(string) error { return nil },
	}
	thenCalls("Let me look.").play(t, stream)
	says().play(t, stream)
	if err := stream.Separate(); err != nil {
		t.Fatal(err)
	}
	says("Here it is.").play(t, stream)
	if want := "Let me look.\n\nHere it is."; heard.String() != want {
		t.Fatalf("listener got %q, want %q", heard.String(), want)
	}
}

// A reply held for checking is stopped while the model is still writing the
// loop, not after it reaches its output cap, and none of it is spoken.
func TestALoopingHeldReplyStopsWhileItIsWritten(t *testing.T) {
	var spoken strings.Builder
	stream := &Stream{Speak: func(s string) error { spoken.WriteString(s); return nil }, Think: func(string) error { return nil }}
	for _, hold := range []bool{true, false} {
		spoken.Reset()
		round := stream.NewRound(context.Background(), RoundOptions{Labels: passThrough{}, Markers: passThrough{}, Hold: hold})
		receive := round.Receiver(0, func(api.ChatResponse) error { return nil }, func(int) {})
		var err error
		chunks := 0
		for chunks < 400 && err == nil {
			err = receive(api.ChatResponse{Message: api.Message{Role: "assistant", Content: "Let me make you a picture.\n\n"}})
			chunks++
		}
		if !errors.Is(err, ErrResponseRepetition) || chunks != 3 {
			t.Fatalf("hold %v: stopped after %d chunks with %v, want the third copy to stop it", hold, chunks, err)
		}
		err = round.FinishSpeech(round.ReleaseHeld(false, err))
		want := "Let me make you a picture.\n\n"
		if hold {
			want = ""
		}
		if !errors.Is(err, ErrResponseRepetition) || spoken.String() != want {
			t.Fatalf("hold %v: spoke %q with %v, want %q", hold, spoken.String(), err, want)
		}
	}
}
