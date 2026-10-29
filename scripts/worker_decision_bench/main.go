// Command worker_decision_bench compares real Ollama routing with an optional
// local decision overlapped with addressee resolution. No worker is constructed.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"sync/atomic"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/decision"
)

type replayCase struct {
	ID       string             `json:"id"`
	Input    decision.WorkInput `json:"input"`
	WantWork bool               `json:"want_work"`
	WantTier decision.Tier      `json:"want_tier"`
}

type sample struct {
	ID            string                `json:"id"`
	Rep           int                   `json:"rep"`
	Mode          string                `json:"mode"`
	Address       brain.Addressee       `json:"address"`
	AddressMS     float64               `json:"address_ms"`
	Ready         bool                  `json:"ready"`
	DecisionMS    float64               `json:"decision_ms,omitempty"`
	Proposal      decision.WorkProposal `json:"proposal"`
	DecisionError string                `json:"decision_error,omitempty"`
	Route         string                `json:"route"`
	Tier          string                `json:"tier"`
	Correct       bool                  `json:"correct"`
	AcceptedError bool                  `json:"accepted_error"`
	ModelCalls    int64                 `json:"model_calls"`
	SpeakingCalls int64                 `json:"speaking_calls"`
	FirstTextMS   float64               `json:"first_text_ms,omitempty"`
	ElapsedMS     float64               `json:"elapsed_ms"`
	Text          string                `json:"text,omitempty"`
	Error         string                `json:"error,omitempty"`
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func tiers(in decision.WorkInput) *brain.WorkerTierAvailability {
	return &brain.WorkerTierAvailability{Simple: slices.Contains(in.EnabledTiers, decision.Simple), Medium: slices.Contains(in.EnabledTiers, decision.Medium), Complex: slices.Contains(in.EnabledTiers, decision.Complex)}
}

// Drain both channels: thinking is not speech, and leaving it unread can stall
// a real stream. The route-only, tiered tool session intercepts worker tools;
// delegation is disabled. There is no Agency, worker factory or job queue here.
func speak(ctx context.Context, b *brain.OllamaBrain, in decision.WorkInput, s *sample, start time.Time) error {
	stream, err := b.ThinkStream(ctx, in.Text, brain.StreamOptions{VoiceMode: true, ToolsEnabled: true, RouterToolsPolicy: brain.RouterToolsRouteOnly, WorkerTiers: tiers(in)})
	if err != nil {
		return err
	}
	chunks, thinking := stream.Chunks, stream.Thinking
	for chunks != nil || thinking != nil {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				chunks = nil
				continue
			}
			if chunk != "" && s.FirstTextMS == 0 {
				s.FirstTextMS = ms(time.Since(start))
			}
			s.Text += chunk
		case _, ok := <-thinking:
			if !ok {
				thinking = nil
			}
		}
	}
	result := <-stream.Done
	s.Route, s.Tier = result.Route, string(result.RouteTier)
	return result.Err
}

func replay(ctx context.Context, b *brain.OllamaBrain, p decision.WorkPolicy, calls *atomic.Int64, c replayCase, mode string, rep int, budget time.Duration, admissionOnly bool) sample {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var history []brain.Turn
	var conversation []brain.AddressMessage
	for _, m := range c.Input.Conversation {
		history = append(history, brain.Turn{Role: m.Role, Content: m.Text})
		name, account := "User", "user"
		if m.Role == "assistant" {
			name, account = "Samantha", "agent"
		}
		conversation = append(conversation, brain.AddressMessage{Name: name, Account: account, Text: m.Text})
	}
	b.LoadHistory(history)
	s := sample{ID: c.ID, Rep: rep, Mode: mode}
	before := calls.Load()
	start := time.Now()
	var pending *decision.Pending
	if mode == "overlap" {
		pending = p.Start(ctx, c.Input, budget)
	}
	defer pending.Close()
	address, err := b.ResolveAddressee(ctx, brain.AddressRequest{Participants: []brain.GroupParticipant{{Name: "Samantha"}, {Name: "Jarvis"}}, Current: "Samantha", Speaker: "User", Account: "user", Conversation: conversation, Text: c.Input.Text})
	s.Address, s.AddressMS = address, ms(time.Since(start))
	// These fixtures contain no address changes. Never apply a proposal after
	// a failed/misdirected admission, even if the classifier finished first.
	if err == nil && !admittedHere(address) {
		err = fmt.Errorf("unexpected addressee: %s %s", address.Kind, address.Name)
	}
	if err == nil {
		var result decision.WorkResult
		result, s.Ready = pending.Take()
		if s.Ready {
			s.Proposal, s.DecisionMS = result.Proposal, ms(result.Elapsed)
			if result.Err != nil {
				s.DecisionError = result.Err.Error()
			}
		}
		// Admission-only is a diagnostic control: no speaking prompt or
		// response can alter the cache or next observation's model residency.
		if !admissionOnly && s.Ready && result.Err == nil && result.Proposal.Accepted {
			s.Route, s.Tier = "spawn", string(result.Proposal.Tier)
		} else if !admissionOnly {
			beforeSpeak := calls.Load()
			err = speak(ctx, b, c.Input, &s, start)
			s.SpeakingCalls = calls.Load() - beforeSpeak
		}
	}
	s.ElapsedMS, s.ModelCalls = ms(time.Since(start)), calls.Load()-before
	if err != nil {
		s.Error = err.Error()
	}
	gotWork := s.Route == "spawn" || s.Route == "narrate"
	s.Correct = err == nil && gotWork == c.WantWork && (!gotWork || s.Tier == string(c.WantTier))
	s.AcceptedError = s.Proposal.Accepted && !s.Correct
	if admissionOnly {
		s.Correct, s.AcceptedError = err == nil, false
	}
	return s
}

func admittedHere(address brain.Addressee) bool {
	return address.Kind == "none" || (address.Kind == "address" && address.Name == "Samantha" && !address.Bare)
}

func comparisonModes(caseIndex, rep int) []string {
	modes := []string{"baseline", "overlap"}
	if (caseIndex+rep)%2 == 1 {
		slices.Reverse(modes)
	}
	return modes
}

func localProxy(base *url.URL, calls *atomic.Int64) *httptest.Server {
	proxy := httputil.NewSingleHostReverseProxy(base)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	proxy.Transport = transport
	proxy.ModifyResponse = func(response *http.Response) error {
		if response.StatusCode >= 300 && response.StatusCode < 400 {
			return errors.New("local Ollama replay refused an HTTP redirect")
		}
		return nil
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" && r.URL.Path != "/api/tags" {
			http.Error(w, "lab endpoint denied", http.StatusForbidden)
			return
		}
		if r.URL.Path == "/api/chat" {
			calls.Add(1)
		}
		proxy.ServeHTTP(w, r)
	}))
}

func run() error {
	casePath := flag.String("cases", "", "fresh labeled synthetic corpus")
	out := flag.String("out", "", "new JSONL output (never overwritten)")
	host := flag.String("ollama-host", "http://127.0.0.1:11434", "local Ollama origin")
	model := flag.String("model", "qwen3.8:latest", "installed speaking model")
	endpoint := flag.String("endpoint", "http://127.0.0.1:18763/v1/systemone", "local decision endpoint")
	reps := flag.Int("reps", 4, "rotated paired passes (even counts balance each case's order)")
	startRep := flag.Int("start-rep", 0, "first pass index when extending a retained comparison")
	budget := flag.Duration("budget", 750*time.Millisecond, "decision inference budget; never a route wait")
	admissionOnly := flag.Bool("admission-only", false, "isolate addressee contention; no speaking calls or route adoption")
	flag.Parse()
	if *casePath == "" || *out == "" || *reps < 1 || *startRep < 0 || *budget <= 0 {
		return errors.New("cases, out and positive reps/budget required")
	}
	base, err := url.Parse(*host)
	if err != nil || base.Scheme != "http" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || (base.Path != "" && base.Path != "/") || !net.ParseIP(base.Hostname()).IsLoopback() {
		return errors.New("ollama must use a literal loopback HTTP origin")
	}
	raw, err := os.ReadFile(*casePath)
	if err != nil {
		return err
	}
	var cases []replayCase
	if err = json.Unmarshal(raw, &cases); err != nil {
		return err
	}
	ids := map[string]bool{}
	if len(cases) == 0 {
		return errors.New("empty cases")
	}
	for _, c := range cases {
		if c.ID == "" || ids[c.ID] || c.Input.Text == "" || (c.WantWork && c.WantTier == "") {
			return fmt.Errorf("invalid or duplicate case: %s", c.ID)
		}
		ids[c.ID] = true
	}
	evaluator, err := decision.NewSystemOne(decision.SystemOneConfig{Provider: "local", Endpoint: *endpoint})
	if err != nil {
		return err
	}
	policy := decision.WorkPolicy{Evaluator: evaluator}
	f, err := os.OpenFile(*out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	emit := func(v any) error {
		if err := enc.Encode(v); err != nil {
			return err
		}
		return f.Sync()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	// Count actual /api/chat requests, including retries, rather than treating
	// a ThinkStream invocation or usage callback as proof of one model call.
	var calls atomic.Int64
	server := localProxy(base, &calls)
	defer server.Close()
	tmp, err := os.MkdirTemp("", "worker-decision-bench-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	b, err := brain.NewOllama(&config.Config{BrainProvider: "ollama", OllamaHost: server.URL, OllamaModel: *model, OllamaNumCtx: 32768, OllamaNumBatch: 128, OllamaKeepAlive: "20m", Persona: "samantha", AgentName: "Samantha", WorkDir: tmp, PromptsDir: tmp})
	if err != nil {
		return err
	}
	hash := b.FreezePersonaPrompt()
	questions, _ := json.Marshal(decision.WorkRequest(decision.WorkInput{}).Questions)
	scope := "admission + route/text; no audio"
	if *admissionOnly {
		scope = "admission only; no speaking calls, route adoption or audio"
	}
	if err = emit(map[string]any{"type": "manifest", "created_at": time.Now().UTC(), "cases_sha256": fmt.Sprintf("%x", sha256.Sum256(raw)), "questions_sha256": fmt.Sprintf("%x", sha256.Sum256(questions)), "persona_hash": hash, "model": *model, "endpoint": *endpoint, "reps": *reps, "start_rep": *startRep, "cases": len(cases), "budget_ms": budget.Milliseconds(), "num_ctx": 32768, "num_batch": 128, "dispatch": "route_only; no Agency or workers", "scope": scope}); err != nil {
		return err
	}
	warm := replayCase{ID: "warmup", Input: decision.WorkInput{Text: "What is a compiler?", EnabledTiers: []decision.Tier{decision.Simple, decision.Medium, decision.Complex}}}
	wctx, wcancel := context.WithTimeout(ctx, 15*time.Second)
	ws := time.Now()
	wp, we := policy.Decide(wctx, warm.Input)
	wcancel()
	errText := ""
	if we != nil {
		errText = we.Error()
	}
	if err = emit(map[string]any{"type": "decision_warmup", "elapsed_ms": ms(time.Since(ws)), "proposal": wp, "error": errText}); err != nil {
		return err
	}
	if we != nil {
		return we
	}
	wsample := replay(ctx, b, policy, &calls, warm, "baseline", -1, *budget, *admissionOnly)
	if err = emit(wsample); err != nil {
		return err
	}
	if wsample.Error != "" {
		return errors.New(wsample.Error)
	}
	for rep := *startRep; rep < *startRep+*reps; rep++ {
		for i := range cases {
			caseIndex := (i + rep) % len(cases)
			c := cases[caseIndex]
			for _, mode := range comparisonModes(caseIndex, rep) {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				s := replay(ctx, b, policy, &calls, c, mode, rep, *budget, *admissionOnly)
				if err = emit(s); err != nil {
					return err
				}
				fmt.Printf("%s rep=%d %s %.1fms calls=%d accepted=%v correct=%v error=%s\n", c.ID, rep, mode, s.ElapsedMS, s.ModelCalls, s.Proposal.Accepted, s.Correct, s.Error)
			}
		}
	}
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
