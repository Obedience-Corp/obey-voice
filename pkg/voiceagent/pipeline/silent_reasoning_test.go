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
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/textclean"
)

func scriptedStreamWithThinking(ctx context.Context, chunks, thinking []string, doneErr error) *brain.Stream {
	out := make(chan string)
	th := make(chan string)
	done := make(chan brain.StreamResult, 1)
	go func() {
		defer close(out)
		defer close(th)
		defer close(done)
		for _, t := range thinking {
			select {
			case <-ctx.Done():
				done <- brain.StreamResult{Err: ctx.Err()}
				return
			case th <- t:
			}
		}
		for _, c := range chunks {
			select {
			case <-ctx.Done():
				done <- brain.StreamResult{Err: ctx.Err()}
				return
			case out <- c:
			}
		}
		done <- brain.StreamResult{Err: doneErr}
	}()
	return &brain.Stream{Chunks: out, Thinking: th, Done: done}
}

func ollamaMessageServer(t *testing.T, messages []map[string]any) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/api/tags") {
			_, _ = io.WriteString(w, `{"models":[{"name":"m"}]}`)
			return
		}
		for i, msg := range messages {
			line, err := json.Marshal(map[string]any{
				"model":   "m",
				"message": msg,
				"done":    i == len(messages)-1,
			})
			if err != nil {
				t.Errorf("marshal message: %v", err)
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

func TestSilentReasoningTaggedThinkIsNotSpoken(t *testing.T) {
	b, err := brain.NewOllama(&config.Config{
		OllamaHost: ollamaDeltaServer(t, []string{
			"<think>The user said hi. Plan a warm greeting.</think>",
			"Hello there!",
		}),
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

	if strings.Contains(deltas.String(), "<think>") {
		t.Errorf("ResponseDelta carries <think>: %q", deltas.String())
	}
	if !strings.Contains(thinks.String(), "The user said hi") {
		t.Errorf("ThinkingDelta missing inner text: %q", thinks.String())
	}
	spoken := strings.Join(provider.Calls(), " ")
	if strings.Contains(spoken, "The user said hi") || textclean.ContainsThinkTag(spoken) {
		t.Errorf("spoken carries reasoning: %q", spoken)
	}
	if !strings.Contains(transcript, "Hello there") {
		t.Errorf("transcript lost the reply: %q", transcript)
	}
}

func TestSilentReasoningThinkingFieldNotSynthesized(t *testing.T) {
	const thinkingBody = "Okay, the user just asked \"Hey, how's it going?\" and then said \"Okay.\""
	b, err := brain.NewOllama(&config.Config{
		OllamaHost: ollamaMessageServer(t, []map[string]any{
			{"role": "assistant", "thinking": thinkingBody, "content": "Hello there!"},
		}),
		OllamaModel: "m",
		MaxHistory:  10,
	})
	if err != nil {
		t.Fatalf("NewOllama: %v", err)
	}

	provider := &progressiveFakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	p := &Pipeline{TTS: provider, Player: player, Events: events.NewBus()}

	var thinks strings.Builder
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

	if thinks.String() != thinkingBody {
		t.Errorf("ThinkingDelta = %q, want the field", thinks.String())
	}
	spoken := strings.Join(provider.Calls(), " ")
	if strings.Contains(spoken, thinkingBody) || strings.Contains(spoken, "just asked") {
		t.Errorf("TTS spoke thinking body: %q", spoken)
	}
	if !strings.Contains(transcript, "Hello there") {
		t.Errorf("transcript = %q, want the greeting", transcript)
	}
}

func TestSilentReasoningSynthQueueNeverCarriesThinkTags(t *testing.T) {
	provider := &progressiveFakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	p := &Pipeline{TTS: provider, Player: player, Events: events.NewBus()}

	var segments, voices []string
	events.Subscribe(p.Events, func(e events.SpeechSegmentReady) { segments = append(segments, e.Text) })
	events.Subscribe(p.Events, func(e events.GeneratingVoice) { voices = append(voices, e.Sentence) })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	metrics := newTurnMetrics()
	_, _, err := p.streamResponse(ctx, cancel, scriptedStream(ctx, []string{
		"<think>secret plan. More reasoning.</think>Hello there!",
	}, nil), false, metrics, p.newTurnConductor(metrics))
	if err != nil {
		t.Fatalf("streamResponse: %v", err)
	}

	for dest, items := range map[string][]string{"synth": provider.Calls(), "segment": segments, "voice": voices} {
		for _, s := range items {
			if textclean.ContainsThinkTag(s) {
				t.Errorf("%s item carries think tag: %q", dest, s)
			}
			if strings.Contains(s, "secret plan") {
				t.Errorf("%s item carries reasoning: %q", dest, s)
			}
		}
	}
}

func TestSilentReasoningRecoverStallWithOnlyThinking(t *testing.T) {
	bus := events.NewBus()
	var got []string
	events.Subscribe(bus, func(e events.ResponseReady) { got = append(got, e.Response) })

	p := &Pipeline{Events: bus}
	metrics := newTurnMetrics()
	turn := p.newTurnConductor(metrics)
	p.recoverTurn(context.Background(), turn, metrics, "", errPlaybackStalled)
	if len(got) != 1 || got[0] != "" {
		t.Fatalf("stall with only thinking: ResponseReady = %q, want empty", got)
	}
}

func TestSilentReasoningThinkingChannelNotSynthesizedWhenNotInThink(t *testing.T) {
	provider := &progressiveFakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	p := &Pipeline{TTS: provider, Player: player, Events: events.NewBus()}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	const thinkingBody = "The user just asked how it's going. I should greet them warmly."
	metrics := newTurnMetrics()
	_, _, err := p.streamResponse(ctx, cancel, scriptedStreamWithThinking(ctx, []string{"Hello there!"}, []string{thinkingBody}, nil), false, metrics, p.newTurnConductor(metrics))
	if err != nil {
		t.Fatalf("streamResponse: %v", err)
	}

	spoken := strings.Join(provider.Calls(), " ")
	if strings.Contains(spoken, thinkingBody) || strings.Contains(spoken, "just asked") {
		t.Errorf("synthQueue saw thinking body while InThink is false: %q", spoken)
	}
	if !strings.Contains(spoken, "Hello there") {
		t.Errorf("lost the greeting: %q", spoken)
	}
}

func TestSilentReasoningLeakFixtureNotSynthesized(t *testing.T) {
	provider := &progressiveFakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	p := &Pipeline{TTS: provider, Player: player, Events: events.NewBus()}

	var thinkErrs int
	events.Subscribe(p.Events, func(e events.Error) {
		if e.Stage == "think" {
			thinkErrs++
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	metrics := newTurnMetrics()
	_, _, err := p.streamResponse(ctx, cancel, scriptedStream(ctx, []string{"<think>escaped tag still here</think>Hi there."}, nil), false, metrics, p.newTurnConductor(metrics))
	if err != nil {
		t.Fatalf("streamResponse: %v", err)
	}
	if thinkErrs == 0 {
		t.Error("want think-leak Error for escaped tags on Chunks")
	}
	if metrics.thinkLeak == 0 {
		t.Error("want thinkLeak > 0 for escaped tags")
	}
	spoken := strings.Join(provider.Calls(), " ")
	if strings.Contains(spoken, "escaped tag") || textclean.ContainsThinkTag(spoken) {
		t.Errorf("leaked tag was synthesized: %q", spoken)
	}
}
