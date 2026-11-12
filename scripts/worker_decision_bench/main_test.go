package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/decision"
)

func TestOllamaReplayDoesNotFollowRedirects(t *testing.T) {
	var escaped atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { escaped.Add(1) }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	base, _ := url.Parse(origin.URL)
	var calls atomic.Int64
	proxy := localProxy(base, &calls, nil)
	defer proxy.Close()
	response, err := http.Get(proxy.URL + "/api/chat")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadGateway || escaped.Load() != 0 || calls.Load() != 1 {
		t.Fatalf("status=%d escaped=%d calls=%d", response.StatusCode, escaped.Load(), calls.Load())
	}
}

func TestAdmissionKeepsCurrentPersonaAndRejectsOtherTargets(t *testing.T) {
	for _, tc := range []struct {
		address brain.Addressee
		want    bool
	}{
		{brain.Addressee{Kind: "none"}, true},
		{brain.Addressee{Kind: "address", Name: "Samantha"}, true},
		{brain.Addressee{Kind: "address", Name: "Samantha", Bare: true}, false},
		{brain.Addressee{Kind: "address", Name: "Jarvis"}, false},
		{brain.Addressee{Kind: "ambiguous"}, false},
	} {
		if got := admittedHere(tc.address); got != tc.want {
			t.Errorf("%+v admitted=%v", tc.address, got)
		}
	}
}

func TestPairedOrderReversesForEachCaseAcrossPasses(t *testing.T) {
	// Corpus rotation must not accidentally cancel the order reversal for a
	// case. Otherwise every comparison for that case gives one mode the warm
	// prefix cache, and its apparent latency change is confounded by ordering.
	const n = 22
	previous := make(map[int]string)
	for rep := 0; rep < 4; rep++ {
		baselineFirst := 0
		for i := 0; i < n; i++ {
			index := (i + rep) % n
			modes := comparisonModes(index, rep)
			if modes[0] == previous[index] {
				t.Fatalf("case %d kept the same order in rep %d", index, rep)
			}
			if modes[0] == "baseline" {
				baselineFirst++
			}
			previous[index] = modes[0]
		}
		if baselineFirst != n/2 {
			t.Fatalf("unbalanced pass %d: %d baseline-first pairs", rep, baselineFirst)
		}
	}
}

// Even an unsolicited executable tool from the real brain must become an
// inert route in this lab. This checks the execution boundary, not model skill.
func TestSpeakingReplayCannotExecuteWorkerTool(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "must-not-exist")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			fmt.Fprint(w, `{"models":[{"name":"fixture"}]}`)
			return
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		tools, _ := json.Marshal(request["tools"])
		var offered []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}
		if err := json.Unmarshal(tools, &offered); err != nil || len(offered) != 1 || offered[0].Function.Name != "route" {
			t.Errorf("expected route-only schema, got %s", tools)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "fixture", "done": true, "message": map[string]any{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{"function": map[string]any{"name": "write_file", "arguments": map[string]any{"path": marker, "content": "unexpected execution"}}}}}})
	}))
	defer server.Close()
	b, err := brain.NewOllama(&config.Config{OllamaHost: server.URL, OllamaModel: "fixture", Persona: "samantha", WorkDir: dir, PromptsDir: dir, MaxHistory: 20})
	if err != nil {
		t.Fatal(err)
	}
	b.FreezePersonaPrompt()
	var got sample
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := speak(ctx, b, decision.WorkInput{Text: "Save this note", EnabledTiers: []decision.Tier{decision.Simple}}, &got, time.Now(), false); err != nil {
		t.Fatal(err)
	}
	if got.Route != "spawn" {
		t.Fatalf("unsolicited tool was not intercepted: %+v", got)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("lab executed a file tool: %v", err)
	}
}

// The router comparison runs the same turn with the route tool and with the
// tools-free JSON route, and records each request's shape and Ollama timing.
// This checks the recording against a fake server, not routing quality.
func TestRouterComparisonRecordsEachRequest(t *testing.T) {
	dir := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			fmt.Fprint(w, `{"models":[{"name":"fixture"}]}`)
			return
		}
		var request struct {
			Tools  []json.RawMessage `json:"tools"`
			Format json.RawMessage   `json:"format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		message := map[string]any{"role": "assistant", "content": "A compiler translates code."}
		switch {
		case len(request.Tools) > 0:
			message = map[string]any{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{"function": map[string]any{"name": "route", "arguments": map[string]any{"kind": "chat", "tier": "simple"}}}}}
		case len(request.Format) > 0:
			message["content"] = `{"kind":"chat","tier":"simple"}`
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "fixture", "done": true, "message": message, "prompt_eval_count": 40, "prompt_eval_duration": 2_000_000, "eval_count": 5})
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	var calls atomic.Int64
	var rec chatRecorder
	proxy := localProxy(base, &calls, &rec)
	defer proxy.Close()
	b, err := brain.NewOllama(&config.Config{OllamaHost: proxy.URL, OllamaModel: "fixture", Persona: "samantha", WorkDir: dir, PromptsDir: dir, MaxHistory: 20})
	if err != nil {
		t.Fatal(err)
	}
	b.FreezePersonaPrompt()
	c := replayCase{ID: "chat", Input: decision.WorkInput{Text: "What is a compiler?", EnabledTiers: []decision.Tier{decision.Simple}}}
	want := map[string][]string{"route_only": {"tools", "plain"}, "json_route": {"schema", "plain"}}
	for _, mode := range routerModes {
		s := replayRouter(t.Context(), b, &rec, c, mode, 0)
		kinds := make([]string, len(s.Requests))
		for i, r := range s.Requests {
			kinds[i] = r.Kind
			if r.PromptEvalCount != 40 || r.PromptEvalMS != 2 || r.EvalCount != 5 {
				t.Fatalf("%s request %d metrics: %+v", mode, i, r)
			}
		}
		if s.Error != "" || !s.Correct || s.Route != "" || !slices.Equal(kinds, want[mode]) || s.ModelCalls != 2 {
			t.Fatalf("%s: %+v kinds=%v", mode, s, kinds)
		}
	}
	if calls.Load() != 4 {
		t.Fatalf("calls=%d", calls.Load())
	}
	for rep := range 2 {
		if a, b := pairedModes(routerModes, 0, rep), pairedModes(routerModes, 0, rep+1); a[0] == b[0] {
			t.Fatal("router modes must alternate order across passes")
		}
	}
}
