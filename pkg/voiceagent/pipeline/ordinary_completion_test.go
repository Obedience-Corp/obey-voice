package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/work"
)

type ordinaryResultBrain struct {
	handoffCompletionBrain
	options brain.StreamOptions
}

func (b *ordinaryResultBrain) ThinkStream(ctx context.Context, input string, opts brain.StreamOptions) (*brain.Stream, error) {
	b.options = opts
	b.mu.Lock()
	b.history = append(b.history, brain.Turn{Role: "user", Content: input}, brain.Turn{Role: "assistant", Content: "Bring a jacket."})
	b.mu.Unlock()
	return scriptedStream(ctx, []string{"Bring a jacket."}, nil), nil
}

func TestOrdinaryCompletionPreservesToolTurnAndRequiresDelivery(t *testing.T) {
	for _, mode := range []string{"text", "typed_voice", "spoken_voice", "failed_tts", "missing_receipt"} {
		t.Run(mode, func(t *testing.T) {
			p, c, scope := newCompletionLeasePipeline(t, false)
			b := &ordinaryResultBrain{}
			p.Brain = b
			p.VoiceToolsEnabled = true
			if mode != "text" {
				p.TTS = &fakeTTS{}
				p.Player = &fencedHandoffPlayer{}
			}
			if mode == "failed_tts" {
				p.TTS = &errTTS{err: errors.New("synthesis unavailable")}
			}
			if mode == "missing_receipt" {
				player := newFakePlayer(time.Millisecond)
				t.Cleanup(func() { _ = player.Close() })
				p.Player = player
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if mode == "spoken_voice" {
				p.STT = &fakeSTT{text: "Should I bring a jacket?"}
				if _, err := p.RunTurn(ctx); err != nil {
					t.Fatal(err)
				}
			} else if err := p.RunTurnTextMode(ctx, "Should I bring a jacket?"); err != nil {
				t.Fatal(err)
			}
			if !b.options.ToolsEnabled || len(b.options.WorkResults) != 1 || b.options.WorkResults[0].Brief != "pending result" {
				t.Fatalf("ordinary request lost tools or projection: %+v", b.options)
			}
			if b.requestCount() != 0 {
				t.Fatal("ordinary update added a completion model call")
			}
			if mode == "failed_tts" || mode == "missing_receipt" {
				pending := c.Pending(scope)
				if len(pending) != 1 || pending[0].State != work.Held {
					t.Fatalf("incomplete speech consumed result: %+v", pending)
				}
				return
			}
			s := c.Snapshot(scope)
			if len(s.Pending) != 0 || len(s.Communicated) != 1 {
				t.Fatalf("ordinary result not communicated: %+v", s)
			}
			if h := b.History(); len(h) != 3 || !strings.Contains(h[2].Content, "pending result") {
				t.Fatalf("appended history=%+v", h)
			}
			if mode != "text" && !s.Communicated[0].LastAttempt.Receipt.PlaybackComplete {
				t.Fatal("voice update lacks full playback")
			}
		})
	}
}

func TestOrdinaryCompletionCancelJoinsWrapperWithOpenProviderChannels(t *testing.T) {
	p, _, _ := newCompletionLeasePipeline(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	_, o, err := p.beginOrdinaryCompletion(ctx, "question", "")
	if err != nil || o == nil {
		t.Fatalf("claim=%v %v", o, err)
	}
	o.wrap(&brain.Stream{Chunks: make(chan string), Done: make(chan brain.StreamResult)}) // nil Thinking, provider stalls
	cancel()
	done := make(chan struct{})
	go func() { o.close(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("canceled stream wrapper did not join")
	}
	if o.lease.Valid() {
		t.Fatal("cancellation left PCM authority valid")
	}
}
