package decider

import (
	"context"
	"encoding/json"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/decision"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNativeLabelReadout(t *testing.T) {
	for _, variant := range []string{"complete", "fallback", "missing", "truncated"} {
		t.Run(variant, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var input struct {
					Count int  `json:"n_probs"`
					Post  bool `json:"post_sampling_probs"`
				}
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					t.Error(err)
				}
				calls++
				expected := 256
				if calls == 2 {
					expected = 248320
				}
				if input.Count != expected || input.Post {
					t.Errorf("unexpected readout: %+v", input)
				}
				top := []map[string]any{{"id": 42, "logprob": -2.0}}
				if variant == "complete" || (variant == "fallback" && calls == 2) {
					top = append(top, map[string]any{"id": 43, "logprob": -4.0})
				}
				json.NewEncoder(w).Encode(map[string]any{"truncated": variant == "truncated", "completion_probabilities": []any{map[string]any{"top_logprobs": top}}})
			}))
			defer server.Close()
			n := nativeScorer{client: server.Client(), base: server.URL}
			got, err := n.scoreLabels(t.Context(), []int{1, 2}, []int{42, 43}, 2)
			if variant == "missing" || variant == "truncated" {
				if err == nil {
					t.Fatal("accepted incomplete readout")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if math.Abs(got[0]-1/(1+math.Exp(-1))) > 1e-12 || math.Abs(got[0]+got[1]-1) > 1e-12 {
				t.Fatalf("temperature softmax: %v", got)
			}
		})
	}
}

func TestNativeLiquidConfidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"answers":{"route":{"choice":"yes","confidence":0.8,"probabilities":{"yes":0.9,"no":0.1}}}}`))
	}))
	defer server.Close()
	n := nativeScorer{client: server.Client(), base: server.URL, model: Model{Engine: "liquid"}}
	out, err := n.evaluate(t.Context(), json.RawMessage(`{}`), map[string]decision.Question{"route": {Type: "choice", Instructions: "Choose", Criteria: map[string]string{"yes": "", "no": ""}}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Answers["route"].Confidence == nil || *out.Answers["route"].Confidence != 0.9 {
		t.Fatalf("changed Liquid confidence contract: %+v", out)
	}
}

func TestNativeAdapterBoundaryAndCancellation(t *testing.T) {
	entered, canceled := make(chan struct{}), make(chan struct{})
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(entered)
		<-r.Context().Done()
		close(canceled)
	}))
	defer worker.Close()
	n := &nativeScorer{client: worker.Client(), base: worker.URL, model: Model{Engine: "liquid"}}
	handler := nativeHandler(n, n.model, "cpu")
	for _, tc := range []struct {
		path, body, origin string
		code               int
	}{
		{"/completion", `{}`, "", 404},
		{"/v1/systemone", `{}`, "https://example.com", 403},
		{"/v1/systemone", `{"path":"/tmp/model"}`, "", 400},
		{"/v1/systemone", `{} {}`, "", 400},
		{"/v1/systemone", strings.Repeat(" ", 1<<20) + `{}`, "", 400},
	} {
		req := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
		req.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != tc.code {
			t.Fatalf("%s: got %d want %d", tc.path, w.Code, tc.code)
		}
	}
	body := `{"state":{},"questions":{"q":{"type":"noul","instructions":"Yes?"}}}`
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/systemone", strings.NewReader(body)).WithContext(ctx))
	}()
	<-entered
	busy := httptest.NewRecorder()
	handler.ServeHTTP(busy, httptest.NewRequest("POST", "/v1/systemone", strings.NewReader(body)))
	if busy.Code != 503 {
		t.Fatalf("busy=%d", busy.Code)
	}
	cancel()
	<-done
	<-canceled
}
