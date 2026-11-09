package decider

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/decision"
)

type scorerEvaluator struct{ scorer *nativeScorer }

func (s scorerEvaluator) Evaluate(ctx context.Context, r decision.Request) (decision.Response, error) {
	raw, err := json.Marshal(r.State)
	if err != nil {
		return decision.Response{}, err
	}
	return s.scorer.evaluate(ctx, raw, r.Questions)
}

// Opt-in real-model qualification; only talks to an explicitly supplied
// disposable loopback worker. It never installs or starts a model itself.
func TestNativeScorerLive(t *testing.T) {
	endpoint := os.Getenv("OBEY_DECIDER_NATIVE_TEST_URL")
	if endpoint == "" {
		t.Skip("isolated native worker not supplied")
	}
	model, err := ModelFor(os.Getenv("OBEY_DECIDER_NATIVE_TEST_MODEL"))
	if err != nil {
		t.Fatal(err)
	}
	n := &nativeScorer{client: &http.Client{}, base: endpoint, model: model}
	var cases []struct {
		ID    string             `json:"id"`
		State decision.WorkInput `json:"state"`
		Want  struct {
			Work bool          `json:"work"`
			Tier decision.Tier `json:"tier"`
		} `json:"want"`
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "decision", "worker-routing.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	var output *os.File
	if path := os.Getenv("OBEY_DECIDER_NATIVE_TEST_OUTPUT"); path != "" {
		output, err = os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		defer output.Close()
	}
	for _, mode := range []string{decision.PolicyRouteChoice, decision.PolicyIntentAndTier} {
		for _, c := range cases {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			start := time.Now()
			got, err := (decision.WorkPolicy{Evaluator: scorerEvaluator{n}, Mode: mode}).Decide(ctx, c.State)
			cancel()
			if err != nil {
				t.Fatalf("%s/%s: %v", mode, c.ID, err)
			}
			if output != nil {
				_ = json.NewEncoder(output).Encode(map[string]any{"id": c.ID, "mode": mode, "got": got, "elapsed_ms": float64(time.Since(start).Microseconds()) / 1000})
			}
		}
	}

}

func TestMapikaStateRendering(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`{"b": "<hi>", "a":[1,true,null]}`, `{"b": "<hi>", "a": [1, true, null]}`},
		{`"hello"`, `hello`},
		{`[0,1,2,3,4,5,6,7]`, `[{"_index": 0, "value": 0}, {"_index": 1, "value": 1}, {"_index": 2, "value": 2}, {"_index": 3, "value": 3}, {"_index": 4, "value": 4}, {"_index": 5, "value": 5}, {"_index": 6, "value": 6}, {"_index": 7, "value": 7}]`},
	} {
		got, err := renderMapikaState([]byte(tc.in))
		if err != nil || got != tc.want {
			t.Fatalf("got %q, %v; want %q", got, err, tc.want)
		}
	}
}
