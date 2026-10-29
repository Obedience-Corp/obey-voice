package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/ollama/ollama/api"
)

// Exercise real Ollama serialization and history ownership against an isolated
// protocol server. The failing-summary case reproduced the old every-turn loop.
func TestAutomaticCompactBudgetDoesNotLoop(t *testing.T) {
	for _, tc := range []struct {
		name   string
		tokens int
		fail   bool
	}{{"soft pressure", 6963, false}, {"hard pressure", 7424, false}, {"summary failure", 6963, true}} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/tags" {
					_, _ = fmt.Fprint(w, `{"models":[{"name":"m"}]}`)
					return
				}
				var req api.ChatRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				calls.Add(1)
				bytes := 0
				for _, m := range req.Messages {
					bytes += len(m.Content) + 16
				}
				if (bytes+3)/4 > 8192*9/10 {
					t.Errorf("oversized summary request: %d tokens", (bytes+3)/4)
				}
				if len(req.Tools) > 0 {
					t.Error("compact enabled tools")
				}
				if tc.fail && req.Stream != nil && !*req.Stream {
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = fmt.Fprint(w, `{"error":"summary unavailable"}`)
					return
				}
				_, _ = fmt.Fprintln(w, `{"message":{"role":"assistant","content":"Keep cobalt and the latest request."},"done":true,"done_reason":"stop"}`)
			}))
			defer srv.Close()
			cfg := &config.Config{OllamaHost: srv.URL, OllamaModel: "m", OllamaNumCtx: 8192, ContinuityNumCtxCeil: 8192, ContinuitySoftLimitRatio: .8, ContinuityIntegrityEnabled: true, ContinuityArchiveEnabled: true, MaxHistory: 40, PromptsDir: t.TempDir(), WorkDir: t.TempDir()}
			b, err := brain.NewOllama(cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := brain.CloseProvider(b); err != nil {
					t.Error(err)
				}
			}()
			hist := []brain.Turn{{Role: "user", Content: "remember cobalt"}, {Role: "assistant", Content: ""}, {Role: "user", Content: "latest request"}, {Role: "assistant", Content: "acknowledged"}}
			for range 3 {
				if err := b.ImportHistory(hist); err != nil {
					t.Fatal(err)
				}
				n := tc.tokens - b.SessionInfo().PromptTokens
				hist[1].Content += strings.Repeat("x", max(0, n*4))
			}
			if !b.NeedsContinuityCompact("follow up") {
				t.Fatal("fixture did not reach soft pressure")
			}
			before := b.CanonicalHistory()
			var stages []string
			bus := events.NewBus()
			events.Subscribe(bus, func(e events.CompactionProgress) { stages = append(stages, e.Stage) })
			p := &Pipeline{Brain: b, CompactPrompt: "Summarize.", AutoCompactAfter: 80, Events: bus}
			if err := p.prepareTurn(t.Context(), "follow up", nil, nil); err != nil {
				t.Fatal(err)
			}
			if b.NeedsContinuityCompact("follow up") {
				t.Fatalf("pressure remains: %+v", b.SessionInfo())
			}
			if !reflect.DeepEqual(before, b.CanonicalHistory()) {
				t.Fatal("compaction changed the durable transcript")
			}
			firstCalls := calls.Load()
			if err := p.prepareTurn(t.Context(), "follow up", nil, nil); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != firstCalls {
				t.Fatal("same input triggered another summary")
			}
			if len(stages) == 0 || stages[0] != "trim" {
				t.Fatalf("stages=%v", stages)
			}
			terminal := "complete"
			if tc.fail {
				terminal = "skipped"
				if firstCalls != 1 {
					t.Fatalf("failed-summary path not reached: calls=%d", firstCalls)
				}
			}
			if stages[len(stages)-1] != terminal {
				t.Fatalf("stages=%v", stages)
			}
			if err := p.RunTurnTextMode(t.Context(), "follow up"); err != nil {
				t.Fatalf("ordinary turn after compact: %v", err)
			}

		})
	}
}

type waitingCompactBrain struct {
	compactBrain
	entered chan struct{}
}

func (b *waitingCompactBrain) ThinkFull(ctx context.Context, _ string, _ brain.StreamOptions) (string, error) {
	close(b.entered)
	<-ctx.Done()
	return "", ctx.Err()
}

func TestAutomaticCompactCancellationClearsProgress(t *testing.T) {
	b := &waitingCompactBrain{compactBrain: compactBrain{history: compactHistory()}, entered: make(chan struct{})}
	bus := events.NewBus()
	var stages []string
	events.Subscribe(bus, func(e events.CompactionProgress) { stages = append(stages, e.Stage) })
	p := &Pipeline{Brain: b, CompactPrompt: "S", AutoCompactAfter: 2, Events: bus}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- p.prepareTurn(ctx, "next", nil, nil) }()
	select {
	case <-b.entered:
	case <-ctx.Done():
		t.Fatal("summary did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("summary ignored cancellation")
	}
	if stages[len(stages)-1] != "canceled" {
		t.Fatalf("stages=%v", stages)
	}
	if len(b.loaded) > 0 {
		t.Fatal("canceled compaction reseeded history")
	}
}

type quietCompactBrain struct{ continuityCompactBrain }

func (*quietCompactBrain) ContinuityCompactFiller() bool { return false }

func TestAutomaticCompactSilentByDefault(t *testing.T) {
	b := &quietCompactBrain{continuityCompactBrain{compactBrain: compactBrain{history: compactHistory(), summary: "the gist"}}}
	tts := &fakeTTS{}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	p := &Pipeline{Brain: b, TTS: tts, Player: player, CompactPrompt: "S", AutoCompactAfter: 100}
	metrics := newTurnMetrics()
	turn := p.newTurnConductor(metrics)
	if err := p.prepareTurn(t.Context(), "next", metrics, turn); err != nil {
		t.Fatal(err)
	}
	if len(tts.Texts()) != 0 {
		t.Fatal("silent compact invoked TTS")
	}
}

type canceledCompactPlayer struct{ *fakePlayer }

func (p canceledCompactPlayer) PlayStream(context.Context, *audio.PCMStream) (*audio.Playback, error) {
	return nil, context.Canceled
}

func TestCanceledCompactFillerDoesNotReportPlaybackError(t *testing.T) {
	b := &continuityCompactBrain{compactBrain: compactBrain{history: compactHistory(), summary: "the gist"}}
	player := newFakePlayer(time.Millisecond)
	defer player.Close()
	bus := events.NewBus()
	var reports []events.Error
	events.Subscribe(bus, func(e events.Error) { reports = append(reports, e) })
	p := &Pipeline{Brain: b, TTS: &fakeTTS{}, Player: canceledCompactPlayer{player}, CompactPrompt: "S", AutoCompactAfter: 100, Events: bus}
	metrics := newTurnMetrics()
	turn := p.newTurnConductor(metrics)
	if err := p.prepareTurn(t.Context(), "next", metrics, turn); err != nil {
		t.Fatal(err)
	}
	if len(reports) > 0 {
		t.Fatalf("canceled filler reported errors: %+v", reports)
	}
}
