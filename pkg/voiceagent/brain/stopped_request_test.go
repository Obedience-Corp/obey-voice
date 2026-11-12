package brain

import (
	"strings"
	"testing"

	"github.com/ollama/ollama/api"
)

// stoppedOllama is a brain whose last request recorded reply, after sending sent.
func stoppedOllama(t *testing.T, reply Turn, sent string) *OllamaBrain {
	t.Helper()
	o, _ := imageClaimBrain(t, nil)
	o.cfg.ActivePersona = "ember"
	o.appendMessage(api.Message{Role: "user", Content: "What else?"}, nil)
	o.appendMessage(api.Message{Role: "assistant", Content: reply.Content}, &TurnIdentity{PersonaID: "ember", ResponseID: "r1"}, reply.Disposition)
	o.stopped = ollamaStopped{reply: true, sent: sent}
	return o
}

func TestStoppedOllamaReplyKeepsOnlyWhatTheTurnRead(t *testing.T) {
	sent := "Let me think about w0 w1 w2 "
	for name, tc := range map[string]struct {
		recorded            Turn
		unread              string
		content, projection string
	}{
		"partial":                   {Turn{Content: "Let me think about w0 w1 w2", Disposition: "interrupted"}, "w1 w2 ", "Let me think about w0", "[Incomplete assistant response; not a completed answer.]\nLet me think about w0"},
		"partial read whole":        {Turn{Content: "Let me think about w0 w1 w2", Disposition: "interrupted"}, "", "Let me think about w0 w1 w2", "[Incomplete assistant response; not a completed answer.]\nLet me think about w0 w1 w2"},
		"complete reply cut short":  {Turn{Content: "Let me think about w0 w1 w2."}, "w2 ", "Let me think about w0 w1", "[Incomplete assistant response; not a completed answer.]\nLet me think about w0 w1"},
		"complete reply read whole": {Turn{Content: "Let me think about w0 w1 w2."}, "", "Let me think about w0 w1 w2.", "[Incomplete assistant response; not a completed answer.]\nLet me think about w0 w1 w2."},
	} {
		t.Run(name, func(t *testing.T) {
			o := stoppedOllama(t, tc.recorded, sent)
			o.RecordStoppedRequest(StoppedRequest{Unread: tc.unread, Reply: &Turn{Disposition: "interrupted"}})
			history := o.History()
			last := history[len(history)-1]
			if len(history) != 2 || last.Content != tc.content || last.Disposition != "interrupted" || last.PersonaID != "ember" || last.ResponseID != "r1" {
				t.Fatalf("history = %+v", history)
			}
			if got := o.history[len(o.history)-1]; got.Role != "assistant" || got.Content != tc.projection {
				t.Fatalf("model context = %+v", got)
			}
			if held := o.working[len(o.working)-1]; held.Content != tc.content || held.Disposition != "interrupted" {
				t.Fatalf("working view = %+v", o.working)
			}
		})
	}
}

func TestStoppedOllamaReplyTheTurnReadNoneOfIsDropped(t *testing.T) {
	sent := "Let me think about w0 "
	o := stoppedOllama(t, Turn{Content: "Let me think about w0", Disposition: "interrupted"}, sent)
	o.RecordStoppedRequest(StoppedRequest{Unread: sent})
	if history := o.History(); len(history) != 1 || history[0].Role != "user" || len(o.history) != 1 || len(o.working) != 1 {
		t.Fatalf("history = %+v, model context %+v", history, o.history)
	}
}

func TestStoppedOllamaReplyWithItsOwnReasonKeepsIt(t *testing.T) {
	o := stoppedOllama(t, Turn{Content: "The answer repeats", Disposition: "repetition"}, "The answer repeats")
	before := o.history[len(o.history)-1]
	o.RecordStoppedRequest(StoppedRequest{Reply: &Turn{Disposition: "failed"}})
	if last := o.History()[1]; last.Disposition != "repetition" || o.history[1].Content != before.Content {
		t.Fatalf("reply = %+v", last)
	}
}

func TestStoppedOllamaRouteGetsItsLineBackOnlyWhenItWasNotFollowed(t *testing.T) {
	for _, followed := range []bool{false, true} {
		o, _ := imageClaimBrain(t, []api.Message{{Role: "assistant", ToolCalls: []api.ToolCall{{Function: api.ToolCallFunction{Name: "route", Arguments: mustArgs(map[string]any{"kind": "spawn"})}}}}})
		stream, err := o.ThinkStream(t.Context(), "Build me a site.", StreamOptions{ToolsEnabled: true, RouterToolsPolicy: RouterToolsRouteOnly, Speaker: "speaker-1"})
		if err != nil {
			t.Fatal(err)
		}
		drainStream(t, stream)
		if result := <-stream.Done; result.Route != "spawn" || len(o.History()) != 0 {
			t.Fatalf("route = %+v, history %+v", result, o.History())
		}
		if followed {
			// A followed route records its line with the routed reply.
			continue
		}
		o.RecordStoppedRequest(StoppedRequest{Line: &Turn{Role: "user", Content: "Build me a site."}})
		history := o.History()
		if len(history) != 1 || history[0].Content != "Build me a site." || history[0].Speaker != "speaker-1" || len(o.history) != 1 || !strings.Contains(o.history[0].Content, "Build me a site.") {
			t.Fatalf("history = %+v, model context %+v", history, o.history)
		}
	}
}

func TestStoppedHostedRequestReplacesWhatItsProviderRecorded(t *testing.T) {
	line := Turn{Role: "user", Content: "Say more."}
	reply := Turn{Role: "assistant", Content: "The vault", Disposition: "interrupted", PersonaID: "mira", ResponseID: "r2"}
	earlier := []Turn{{Role: "user", Content: "Hello."}, {Role: "assistant", Content: "Hi.", PersonaID: "mira"}}
	for name, recorded := range map[string][]Turn{
		"nothing recorded":  nil,
		"recorded complete": {{Role: "user", Content: "Say more."}, {Role: "assistant", Content: "The vault code is cobalt.", PersonaID: "mira", ResponseID: "r2"}},
	} {
		t.Run(name, func(t *testing.T) {
			canonical := append(CloneTurns(earlier), recorded...)
			working := CloneTurns(canonical[1:])
			canonical, working = replaceStoppedRequest(canonical, working, len(earlier), StoppedRequest{Line: &line, Reply: &reply})
			want := append(CloneTurns(earlier), line, reply)
			if len(canonical) != 4 || canonical[2].Content != line.Content || canonical[3].Content != reply.Content || canonical[3].Disposition != "interrupted" {
				t.Fatalf("canonical = %+v, want %+v", canonical, want)
			}
			if len(working) != 3 || working[1].Content != line.Content || working[2].Content != reply.Content {
				t.Fatalf("working = %+v", working)
			}
		})
	}
}
