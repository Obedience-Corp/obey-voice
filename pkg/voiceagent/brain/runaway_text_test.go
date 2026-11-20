package brain

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ollama/ollama/api"
)

func TestRequestedLiteralRepetitionIsPreserved(t *testing.T) {
	literal := strings.Repeat("und", 100)
	o := repetitionBrain(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(api.ChatResponse{Done: true, DoneReason: "stop", Message: api.Message{Content: literal}})
	})
	stream, err := o.ThinkStream(t.Context(), "Repeat the syllable und verbatim 100 times.", StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	text, _ := drainStream(t, stream)
	if result := <-stream.Done; result.Err != nil || text != literal {
		t.Fatalf("requested literal was suppressed: text=%q result=%+v", text, result)
	}
}

func TestOllamaRunawaySyllableStopsTransportWithoutRetry(t *testing.T) {
	stopped := make(chan struct{})
	calls := 0
	o := repetitionBrain(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		defer close(stopped)
		_ = json.NewEncoder(w).Encode(api.ChatResponse{Message: api.Message{Content: "Here is the status. camp"}})
		for {
			if err := json.NewEncoder(w).Encode(api.ChatResponse{Message: api.Message{Content: "undundund"}}); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			default:
			}
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	stream, err := o.ThinkStream(ctx, "Summarize the current status.", StreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	text, _ := drainStream(t, stream)
	result := <-stream.Done
	if !errors.Is(result.Err, ErrResponseRepetition) || !result.Recovered || !strings.HasPrefix(text, "Here is the status.") || len(text) > 100 {
		t.Fatalf("runaway not bounded: text=%q result=%+v", text, result)
	}
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("runaway transport remained active")
	}
	if calls != 1 {
		t.Fatalf("runaway generation restarted: %d calls", calls)
	}
	history := o.History()
	last := history[len(history)-1]
	if last.Disposition != "repetition" || last.Content != strings.TrimSpace(text) {
		t.Fatalf("incomplete reply was not attributed correctly: %+v", last)
	}
}
