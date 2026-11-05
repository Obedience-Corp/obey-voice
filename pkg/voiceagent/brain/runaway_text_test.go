package brain

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ollama/ollama/api"
)

func TestRunawayTextGuardAcrossDeltaBoundaries(t *testing.T) {
	for _, size := range []int{1, 17, 10000} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			for _, tc := range []struct {
				text string
				loop bool
			}{
				{"Here is the status. camp" + strings.Repeat("und", 1000), true},
				{"A longer cycle " + strings.Repeat("abcdefghijklmnop", 40), true},
				{strings.Repeat("Repeated headings are ordinary prose. ", 50), false},
				{"A long identifier " + strings.Repeat("abcdefghijklmnopqrstuvwxyz", 30), false},
				{"A literal: `" + strings.Repeat("und", 500) + "`.", false},
				{"A literal containing ticks: `` `" + strings.Repeat("und", 500) + "` ``.", false},
				{"```text\n" + strings.Repeat("a", 1000) + "\n```\nDone.", false},
				{"日本語の文章はそのまま。 Normal words still stream.", false},
			} {
				var guard runawayTextGuard
				var output strings.Builder
				var err error
				for remaining := tc.text; remaining != "" && err == nil; {
					n := min(size, len(remaining))
					var text string
					text, err = guard.feed(remaining[:n])
					output.WriteString(text)
					remaining = remaining[n:]
				}
				output.WriteString(guard.flush())
				if tc.loop {
					if !errors.Is(err, ErrResponseRepetition) || output.Len() > 100 {
						t.Fatalf("runaway escaped: bytes=%d err=%v", output.Len(), err)
					}
				} else if err != nil || output.String() != tc.text {
					t.Fatalf("valid output changed: bytes=%d err=%v", output.Len(), err)
				}
			}
		})
	}
}

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
