package brain

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/discussion"
	"github.com/ollama/ollama/api"
)

func discussionInput() discussion.AudienceRequest {
	return discussion.AudienceRequest{Participants: []discussion.Participant{{Key: "p0", Name: "Jarvis"}, {Key: "p1", Name: "Samantha"}}, Current: "p0", Speaker: "Lance", Latest: "What does everyone think?"}
}
func TestDiscussionInferenceIsPrivatePinnedAndStrict(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		bad        bool
	}{
		{"group", `{"kind":"group","member_keys":["p0","p1"],"coverage":"all","unresolved_names":[],"reason":"explicit"}`, false},
		{"unknown key", `{"kind":"group","member_keys":["p7"],"coverage":"all","unresolved_names":[],"reason":"explicit"}`, true},
		{"unresolved", `{"kind":"group","member_keys":["p0","p1"],"coverage":"all","unresolved_names":["Athena"],"reason":"explicit"}`, true},
		{"missing", `{"kind":"current","coverage":"none","reason":"none"}`, true},
		{"null", `{"kind":"current","member_keys":null,"coverage":"none","unresolved_names":[],"reason":"none"}`, true},
		{"extra", `{"kind":"current","member_keys":[],"coverage":"none","unresolved_names":[],"reason":"none","tools":["shell"]}`, true},
		{"trailing", `{"kind":"current","member_keys":[],"coverage":"none","unresolved_names":[],"reason":"none"}{}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var wire api.ChatRequest
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
					t.Error(err)
				}
				_ = json.NewEncoder(w).Encode(api.ChatResponse{Done: true, Message: api.Message{Content: tc.body}})
			}))
			defer server.Close()
			u, _ := url.Parse(server.URL)
			o := &OllamaBrain{client: api.NewClient(u, server.Client()), model: "coordinator", cfg: &config.Config{OllamaHost: server.URL, OllamaNumCtx: 8192}}
			o.LoadHistory([]Turn{{Role: "user", Content: "Not automatically included"}})
			before := o.History()
			policy, err := o.DiscussionDecider()
			if err != nil {
				t.Fatal(err)
			}
			o.model = "later-speaker"
			_, err = policy.Audience(t.Context(), discussionInput())
			if (err != nil) != tc.bad {
				t.Fatalf("err=%v", err)
			}
			if wire.Model != "coordinator" || len(wire.Tools) != 0 || wire.Stream == nil || *wire.Stream || wire.Think.Value != false || len(wire.Format) == 0 {
				t.Fatalf("private contract violated: %+v", wire)
			}
			if !reflect.DeepEqual(before, o.History()) || strings.Contains(wire.Messages[1].Content, "Not automatically included") {
				t.Fatal("private inference touched or leaked full live history")
			}
		})
	}
}
func TestDiscussionInferenceCancellationAndUnsupportedHost(t *testing.T) {
	o := &OllamaBrain{cfg: &config.Config{OllamaHost: "https://example.com"}}
	if _, err := o.DiscussionDecider(); err == nil {
		t.Fatal("remote inference silently enabled")
	}
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("cancelled request reached server") }))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	o = &OllamaBrain{client: api.NewClient(u, server.Client()), model: "test", cfg: &config.Config{OllamaHost: server.URL}}
	p, err := o.DiscussionDecider()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = p.Audience(ctx, discussionInput())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
}
func TestDiscussionReplyKeepsOneHumanAndCapsOutput(t *testing.T) {
	var wire api.ChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&wire)
		_ = json.NewEncoder(w).Encode(api.ChatResponse{Done: true, DoneReason: "stop", Message: api.Message{Content: "A useful independent view."}})
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	o := &OllamaBrain{client: api.NewClient(u, server.Client()), model: "test", cfg: &config.Config{AgentName: "Jarvis", OllamaNumCtx: 8192, ContinuityIntegrityEnabled: true}}
	if err := o.ImportHistory([]Turn{{Role: "user", Content: "Everyone, views?"}}); err != nil {
		t.Fatal(err)
	}
	state := discussion.New(t.Context(), "Everyone, views?", "all", "invited", []string{"p0", "p1"})
	defer state.End("yielded")
	stream, err := o.ThinkStream(state.Context(), "Everyone, views?", StreamOptions{Discussion: &discussion.Directive{ID: state.ID(), Initial: true, InputAlreadyRecorded: true, State: state}, Identity: &TurnIdentity{PersonaID: "jarvis", PersonaDisplayName: "Jarvis", ResponseID: "reply-1"}, ToolsEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	for range stream.Chunks {
	}
	result := <-stream.Done
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	history := o.History()
	if len(history) != 2 || history[0].Role != "user" || history[1].PersonaID != "jarvis" {
		t.Fatalf("history=%+v", history)
	}
	if len(wire.Tools) != 0 || wire.Options["num_predict"] != float64(discussion.MaxReplyTokens) || wire.Think.Value != false {
		t.Fatalf("unbounded reply=%+v", wire)
	}
	if strings.Contains(wire.Messages[0].Content, "latest agent is handing you the floor") {
		t.Fatal("invented an initial handoff")
	}
}
func TestLiveDiscussionAudienceCorpus(t *testing.T) {
	model := os.Getenv("OBEY_VOICE_LIVE_DISCUSSION_MODEL")
	if model == "" {
		t.Skip("set OBEY_VOICE_LIVE_DISCUSSION_MODEL to an installed local model")
	}
	raw, err := os.ReadFile("testdata/discussion/audience.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		ID    string                     `json:"id"`
		Input discussion.AudienceRequest `json:"context"`
		Want  struct {
			Kind     string   `json:"kind"`
			Members  []string `json:"member_keys"`
			Coverage string   `json:"coverage"`
		} `json:"expect"`
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse("http://127.0.0.1:11434")
	o := &OllamaBrain{client: api.NewClient(u, http.DefaultClient), model: model, cfg: &config.Config{OllamaHost: u.String(), OllamaNumCtx: 8192, PersonaRoutingTimeout: 30}}
	policy, err := o.DiscussionDecider()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.ID, func(t *testing.T) {
			got, err := policy.Audience(t.Context(), tc.Input)
			if err != nil {
				t.Fatal(err)
			}
			if got.Kind != tc.Want.Kind || !reflect.DeepEqual(got.Members, tc.Want.Members) || got.Coverage != tc.Want.Coverage {
				t.Fatalf("got=%+v want=%+v", got, tc.Want)
			}
		})
	}
}

func TestDiscussionStaleFirstOutputDoesNotReachStreamOrHistory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(api.ChatResponse{Done: true, DoneReason: "stop", Message: api.Message{Content: "This stale reply must not escape."}})
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	o := &OllamaBrain{client: api.NewClient(u, server.Client()), model: "fixture", cfg: &config.Config{MaxHistory: 20, OllamaNumCtx: 8192}}
	if err := o.ImportHistory([]Turn{{Role: "user", Content: "Everyone, thoughts?"}}); err != nil {
		t.Fatal(err)
	}
	state := discussion.New(t.Context(), "Everyone, thoughts?", "all", "invited", []string{"p0"})
	defer state.End("superseded")
	stream, err := o.ThinkStream(state.Context(), "Everyone, thoughts?", StreamOptions{Discussion: &discussion.Directive{ID: state.ID(), Original: "Everyone, thoughts?", State: state, BeforeOutput: func(context.Context) error { return discussion.ErrStale }}})
	if err != nil {
		t.Fatal(err)
	}
	for text := range stream.Chunks {
		t.Errorf("stale text released: %q", text)
	}
	result := <-stream.Done
	if !errors.Is(result.Err, discussion.ErrStale) {
		t.Fatalf("result=%+v", result)
	}
	if len(o.History()) != 1 {
		t.Fatalf("stale reply entered canonical history: %+v", o.History())
	}
}

func TestDiscussionReplayIsRejectedWithoutPrivateRetryOrOutput(t *testing.T) {
	prior := strings.Repeat("A local prototype establishes the deployment contract before a service is added. ", 4)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(api.ChatResponse{Done: true, DoneReason: "stop", Message: api.Message{Content: prior}})
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	o := &OllamaBrain{client: api.NewClient(u, server.Client()), model: "fixture", cfg: &config.Config{MaxHistory: 20, OllamaNumCtx: 8192, ActivePersona: "jarvis"}}
	if err := o.ImportHistory([]Turn{{Role: "user", Content: "Everyone, thoughts?"}, {Role: "assistant", Content: prior, PersonaID: "jarvis"}, {Role: "assistant", Content: "Check environment parity.", PersonaID: "samantha"}}); err != nil {
		t.Fatal(err)
	}
	state := discussion.New(t.Context(), "Everyone, thoughts?", "all", "invited", []string{"p0", "p1"})
	defer state.End("yielded")
	stream, err := o.ThinkStream(state.Context(), "Everyone, thoughts?", StreamOptions{AgentContinuation: true, Discussion: &discussion.Directive{ID: state.ID(), Original: "Everyone, thoughts?", State: state}})
	if err != nil {
		t.Fatal(err)
	}
	for text := range stream.Chunks {
		t.Errorf("replayed text released: %q", text)
	}
	result := <-stream.Done
	if result.Err == nil || calls != 1 || len(o.History()) != 3 {
		t.Fatalf("replay was accepted or retried: result=%+v requests=%d turns=%d", result, calls, len(o.History()))
	}
}
