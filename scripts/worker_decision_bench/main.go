// Command worker_decision_bench compares real Ollama routing with an optional
// local decision overlapped with addressee resolution, or the route tool with
// the tools-free JSON route. No worker is constructed.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
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
	Requests      []chatMetrics         `json:"requests,omitempty"`
}

// chatMetrics is one /api/chat request: its shape (tools offered, schema
// format, or neither) and Ollama's timing from the final response line.
type chatMetrics struct {
	Kind            string  `json:"kind"`
	PromptEvalCount int     `json:"prompt_eval_count"`
	PromptEvalMS    float64 `json:"prompt_eval_ms"`
	EvalCount       int     `json:"eval_count"`
	TotalMS         float64 `json:"total_ms"`
}

// chatRecorder keeps every proxied chat request's metrics in arrival order.
type chatRecorder struct {
	mu   sync.Mutex
	seen []chatMetrics
}

func (r *chatRecorder) len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.seen)
}

func (r *chatRecorder) since(n int) []chatMetrics {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.seen[n:])
}

func (r *chatRecorder) add(kind string, response []byte) {
	m := chatMetrics{Kind: kind}
	for line := range strings.SplitSeq(string(response), "\n") {
		var final struct {
			Done               bool  `json:"done"`
			PromptEvalCount    int   `json:"prompt_eval_count"`
			PromptEvalDuration int64 `json:"prompt_eval_duration"`
			EvalCount          int   `json:"eval_count"`
			TotalDuration      int64 `json:"total_duration"`
		}
		if json.Unmarshal([]byte(line), &final) == nil && final.Done {
			m.PromptEvalCount, m.EvalCount = final.PromptEvalCount, final.EvalCount
			m.PromptEvalMS, m.TotalMS = ms(time.Duration(final.PromptEvalDuration)), ms(time.Duration(final.TotalDuration))
		}
	}
	r.mu.Lock()
	r.seen = append(r.seen, m)
	r.mu.Unlock()
}

func requestKind(body []byte) string {
	var req struct {
		Tools  []json.RawMessage `json:"tools"`
		Format json.RawMessage   `json:"format"`
	}
	_ = json.Unmarshal(body, &req)
	switch {
	case len(req.Tools) > 0:
		return "tools"
	case len(req.Format) > 0 && string(req.Format) != "null":
		return "schema"
	default:
		return "plain"
	}
}

// teeWriter copies a proxied response for chatRecorder, up to a bound.
type teeWriter struct {
	http.ResponseWriter
	buf bytes.Buffer
}

func (w *teeWriter) Write(p []byte) (int, error) {
	if w.buf.Len() < 8<<20 {
		w.buf.Write(p)
	}
	return w.ResponseWriter.Write(p)
}

func (w *teeWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func tiers(in decision.WorkInput) *brain.WorkerTierAvailability {
	return &brain.WorkerTierAvailability{Simple: slices.Contains(in.EnabledTiers, decision.Simple), Medium: slices.Contains(in.EnabledTiers, decision.Medium), Complex: slices.Contains(in.EnabledTiers, decision.Complex)}
}

// Drain both channels: thinking is not speech, and leaving it unread can stall
// a real stream. The route-only, tiered tool session intercepts worker tools;
// delegation is disabled. There is no Agency, worker factory or job queue here.
func speak(ctx context.Context, b *brain.OllamaBrain, in decision.WorkInput, s *sample, start time.Time, routeJSON bool) error {
	stream, err := b.ThinkStream(ctx, in.Text, brain.StreamOptions{VoiceMode: true, ToolsEnabled: true, RouterToolsPolicy: brain.RouterToolsRouteOnly, RouteJSON: routeJSON, WorkerTiers: tiers(in)})
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
			err = speak(ctx, b, c.Input, &s, start, false)
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

// replayRouter measures one turn routed by the route tool (route_only) or by
// the tools-free JSON request (json_route). No addressee or decision call is
// made, so every recorded request belongs to the turn.
func replayRouter(ctx context.Context, b *brain.OllamaBrain, rec *chatRecorder, c replayCase, mode string, rep int) sample {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var history []brain.Turn
	for _, m := range c.Input.Conversation {
		history = append(history, brain.Turn{Role: m.Role, Content: m.Text})
	}
	b.LoadHistory(history)
	s := sample{ID: c.ID, Rep: rep, Mode: mode}
	before := rec.len()
	start := time.Now()
	err := speak(ctx, b, c.Input, &s, start, mode == "json_route")
	s.ElapsedMS = ms(time.Since(start))
	s.Requests = rec.since(before)
	s.ModelCalls = int64(len(s.Requests))
	s.SpeakingCalls = s.ModelCalls
	if err != nil {
		s.Error = err.Error()
	}
	gotWork := s.Route == "spawn" || s.Route == "narrate"
	s.Correct = err == nil && gotWork == c.WantWork && (!gotWork || s.Tier == string(c.WantTier))
	return s
}

func admittedHere(address brain.Addressee) bool {
	return address.Kind == "none" || (address.Kind == "address" && address.Name == "Samantha" && !address.Bare)
}

var (
	decisionModes = []string{"baseline", "overlap"}
	routerModes   = []string{"route_only", "json_route"}
)

func comparisonModes(caseIndex, rep int) []string {
	return pairedModes(decisionModes, caseIndex, rep)
}

func pairedModes(pair []string, caseIndex, rep int) []string {
	modes := slices.Clone(pair)
	if (caseIndex+rep)%2 == 1 {
		slices.Reverse(modes)
	}
	return modes
}

func localProxy(base *url.URL, calls *atomic.Int64, rec *chatRecorder) *httptest.Server {
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
			if rec != nil {
				body, err := io.ReadAll(io.LimitReader(r.Body, 32<<20))
				if err != nil {
					http.Error(w, "read request", http.StatusBadRequest)
					return
				}
				r.Body, r.ContentLength = io.NopCloser(bytes.NewReader(body)), int64(len(body))
				tee := &teeWriter{ResponseWriter: w}
				proxy.ServeHTTP(tee, r)
				rec.add(requestKind(body), tee.buf.Bytes())
				return
			}
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
	compare := flag.String("compare", "decision", "decision: baseline vs decision overlap; router: route_only vs json_route, with no decision model or addressee call")
	flag.Parse()
	if *casePath == "" || *out == "" || *reps < 1 || *startRep < 0 || *budget <= 0 {
		return errors.New("cases, out and positive reps/budget required")
	}
	if *compare != "decision" && *compare != "router" {
		return errors.New("compare must be decision or router")
	}
	router := *compare == "router"
	if router && *admissionOnly {
		return errors.New("admission-only applies to the decision comparison")
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
	var policy decision.WorkPolicy
	if !router {
		evaluator, err := decision.NewSystemOne(decision.SystemOneConfig{Provider: "local", Endpoint: *endpoint})
		if err != nil {
			return err
		}
		policy = decision.WorkPolicy{Evaluator: evaluator}
	}
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
	var rec chatRecorder
	server := localProxy(base, &calls, &rec)
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
	if router {
		scope = "route/text only; route_only vs json_route; no admission, decision model or audio"
	}
	if err = emit(map[string]any{"type": "manifest", "created_at": time.Now().UTC(), "cases_sha256": fmt.Sprintf("%x", sha256.Sum256(raw)), "questions_sha256": fmt.Sprintf("%x", sha256.Sum256(questions)), "persona_hash": hash, "model": *model, "endpoint": *endpoint, "reps": *reps, "start_rep": *startRep, "cases": len(cases), "budget_ms": budget.Milliseconds(), "num_ctx": 32768, "num_batch": 128, "dispatch": "route_only; no Agency or workers", "compare": *compare, "scope": scope}); err != nil {
		return err
	}
	warm := replayCase{ID: "warmup", Input: decision.WorkInput{Text: "What is a compiler?", EnabledTiers: []decision.Tier{decision.Simple, decision.Medium, decision.Complex}}}
	if router {
		return runRouter(ctx, b, &rec, cases, warm, *startRep, *reps, emit)
	}
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

func runRouter(ctx context.Context, b *brain.OllamaBrain, rec *chatRecorder, cases []replayCase, warm replayCase, startRep, reps int, emit func(any) error) error {
	wsample := replayRouter(ctx, b, rec, warm, "route_only", -1)
	if err := emit(wsample); err != nil {
		return err
	}
	if wsample.Error != "" {
		return errors.New(wsample.Error)
	}
	for rep := startRep; rep < startRep+reps; rep++ {
		for i := range cases {
			caseIndex := (i + rep) % len(cases)
			c := cases[caseIndex]
			for _, mode := range pairedModes(routerModes, caseIndex, rep) {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				s := replayRouter(ctx, b, rec, c, mode, rep)
				if err := emit(s); err != nil {
					return err
				}
				fmt.Printf("%s rep=%d %s %.1fms calls=%d correct=%v error=%s\n", c.ID, rep, mode, s.ElapsedMS, s.ModelCalls, s.Correct, s.Error)
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
