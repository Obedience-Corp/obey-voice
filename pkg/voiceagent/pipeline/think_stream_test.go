package pipeline

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
)

// ollamaDeltaServer answers the two endpoints NewOllama and ThinkStream need,
// streaming deltas verbatim so a test can shape the exact bytes a model emits.
func ollamaDeltaServer(t *testing.T, deltas []string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/api/tags") {
			_, _ = io.WriteString(w, `{"models":[{"name":"m"}]}`)
			return
		}
		for i, d := range deltas {
			line, err := json.Marshal(map[string]any{
				"model":   "m",
				"message": map[string]any{"role": "assistant", "content": d},
				"done":    i == len(deltas)-1,
			})
			if err != nil {
				t.Errorf("marshal delta: %v", err)
				return
			}
			_, _ = w.Write(append(line, '\n'))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// A reasoning model writes its label after the block: "<think>…</think>\nObey: hi".
// The label check used to resolve on "<think>" — not a label prefix — so the
// head was decided before the reply had said anything and "Obey:" streamed
// straight through to TTS and the transcript while history alone was clean.
//
// This drives the real brain into the real turn loop, so all four destinations
// are asserted from one set of model bytes: what is spoken, what the transcript
// (and so ResponseReady and the session file) carries, what the model reads back
// as history, and what the TUI is given to paint.
func TestReasoningReplyReachesNoDestinationWithALabel(t *testing.T) {
	const reply = "Hello speaker-2! It's a wonderful day."
	tests := map[string]struct {
		deltas    []string
		wantThink bool
	}{
		"label after a reasoning block": {
			deltas:    []string{"<think>The user said hi. ", "Plan a warm greeting.</think>", "\nObey: " + reply},
			wantThink: true,
		},
		"label split across the chunk that closes the block": {
			deltas:    []string{"<think>Reasoning about it.", "</think>\nOb", "ey: " + reply},
			wantThink: true,
		},
		"block and label in one delta": {
			deltas:    []string{"<think>Reasoning about it.</think>\nObey: " + reply},
			wantThink: true,
		},
		"label with no reasoning block": {
			deltas: []string{"Obey: " + reply},
		},
		"reasoning block with no label": {
			deltas:    []string{"<think>Reasoning about it.</think>\n" + reply},
			wantThink: true,
		},
		"neither convention": {
			deltas: []string{reply},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			b, err := brain.NewOllama(&config.Config{
				OllamaHost:  ollamaDeltaServer(t, tt.deltas),
				OllamaModel: "m",
				AgentName:   "Obey",
				MaxHistory:  10,
			})
			if err != nil {
				t.Fatalf("NewOllama: %v", err)
			}

			provider := &progressiveFakeTTS{}
			player := newFakePlayer(time.Millisecond)
			defer player.Close()
			p := &Pipeline{TTS: provider, Player: player, Events: events.NewBus()}

			var deltas, thinks strings.Builder
			events.Subscribe(p.Events, func(d events.ResponseDelta) { deltas.WriteString(d.Text) })
			events.Subscribe(p.Events, func(d events.ThinkingDelta) { thinks.WriteString(d.Text) })

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			stream, err := b.ThinkStream(ctx, "hi", brain.StreamOptions{})
			if err != nil {
				t.Fatalf("ThinkStream: %v", err)
			}
			metrics := newTurnMetrics()
			transcript, _, err := p.streamResponse(ctx, cancel, stream, false, metrics, p.newTurnConductor(metrics))
			if err != nil {
				t.Fatalf("streamResponse: %v", err)
			}

			history := b.History()
			if len(history) == 0 {
				t.Fatal("no assistant turn recorded")
			}
			recorded := history[len(history)-1].Content

			spoken := strings.Join(provider.Calls(), " ")
			for dest, got := range map[string]string{
				"spoken":     spoken,
				"transcript": transcript,
				"history":    recorded,
			} {
				if strings.Contains(strings.ToLower(got), "obey:") {
					t.Errorf("%s carries the agent label: %q", dest, got)
				}
				if strings.Contains(strings.ToLower(got), "<think") || strings.Contains(strings.ToLower(got), "</think") {
					t.Errorf("%s carries think markup: %q", dest, got)
				}
				if strings.Contains(got, "Reasoning about it") || strings.Contains(got, "Plan a warm greeting") {
					t.Errorf("%s carries the reasoning: %q", dest, got)
				}
				if !strings.Contains(got, "Hello speaker-2") {
					t.Errorf("%s lost the reply: %q", dest, got)
				}
			}

			if strings.Contains(deltas.String(), "<think>") || strings.Contains(deltas.String(), "</think>") {
				t.Errorf("ResponseDelta carries think tags: %q", deltas.String())
			}
			if tt.wantThink {
				if thinks.String() == "" {
					t.Errorf("ThinkingDelta empty, want inner think text")
				}
				if strings.Contains(thinks.String(), "<think>") || strings.Contains(thinks.String(), "</think>") {
					t.Errorf("ThinkingDelta carries tags: %q", thinks.String())
				}
			} else if thinks.Len() != 0 {
				t.Errorf("ThinkingDelta = %q, want empty when there is no think block", thinks.String())
			}
			if strings.Contains(strings.ToLower(deltas.String()), "obey:") {
				t.Errorf("deltas carry the agent label: %q", deltas.String())
			}
		})
	}
}

// Closing the sentence channel is its own exit from the turn loop, and two
// endings ride on what it does with the think filter. A reply that stops inside
// an unclosed block must speak none of the reasoning the filter swallowed —
// there is no </think> to prove the block ended, so "drop it" has to be the
// default. And a reply whose last segment is ordinary text carrying no sentence
// terminator must still be spoken: the close is the only thing that flushes it.
//
// Both are correct today and neither has a test, which is the combination that
// regresses quietly — the first ending would leak reasoning into speech, the
// second would silently drop the last thing said.
func TestStreamCloseDrainsThinkFilter(t *testing.T) {
	tests := []struct {
		name   string
		chunks []string
		want   []string
		deny   []string
	}{
		{
			name:   "ends inside an unclosed block",
			chunks: []string{"Hello there. ", "<think>the part nobody should hear"},
			want:   []string{"Hello there."},
			deny:   []string{"nobody should hear", "<think"},
		},
		{
			name:   "unterminated tail after the block still speaks",
			chunks: []string{"<think>plan it</think>", "Goodbye for now"},
			want:   []string{"Goodbye for now"},
			deny:   []string{"plan it", "think>"},
		},
		{
			name:   "nothing but an unclosed block says nothing",
			chunks: []string{"<think>still going"},
			deny:   []string{"still going", "<think"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := &progressiveFakeTTS{}
			player := newFakePlayer(time.Millisecond)
			defer player.Close()
			p := &Pipeline{TTS: provider, Player: player, Events: events.NewBus()}

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			metrics := newTurnMetrics()
			transcript, _, err := p.streamResponse(ctx, cancel, scriptedStream(ctx, tt.chunks, nil), false, metrics, p.newTurnConductor(metrics))
			if err != nil {
				t.Fatalf("streamResponse: %v", err)
			}

			spoken := strings.Join(provider.Calls(), " ")
			for dest, got := range map[string]string{"spoken": spoken, "transcript": transcript} {
				for _, want := range tt.want {
					if !strings.Contains(got, want) {
						t.Errorf("%s lost %q: %q", dest, want, got)
					}
				}
				for _, deny := range tt.deny {
					if strings.Contains(got, deny) {
						t.Errorf("%s carries %q: %q", dest, deny, got)
					}
				}
			}
		})
	}
}
