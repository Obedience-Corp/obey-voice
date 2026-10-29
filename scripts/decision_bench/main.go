// Command decision_bench compares optional classifiers with the real addressee
// resolver. It is a lab harness; it does not change voice-session configuration.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/internal/summon"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

type replayCase struct {
	ID       string               `json:"id"`
	Category string               `json:"category"`
	Request  brain.AddressRequest `json:"request"`
	Want     brain.Addressee      `json:"want"`
}

type answer struct {
	Choice        string             `json:"choice"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
	Noul          *float64           `json:"noul"`
}

type systemOneResponse struct {
	Model   string            `json:"model"`
	Answers map[string]answer `json:"answers"`
}

type candidate struct {
	endpoint, key, model string
	client               *http.Client
	budget               time.Duration
}

func questions(req brain.AddressRequest) map[string]any {
	criteria := map[string]string{
		"none":        "No change of listener. A mention, quotation, hypothetical, or discussion ABOUT a person rather than talking TO them.",
		"ambiguous":   "Several simultaneous listeners, or a name shared by several participants, with no unique immediate addressee.",
		"unavailable": "The immediate listener is a named person who is absent from the participants.",
	}
	for i, p := range req.Participants {
		criteria[fmt.Sprintf("person_%d", i)] = fmt.Sprintf("Talking directly TO %s (aliases: %s), or asking this person to take the next turn. When asked to consult someone else, this person is the immediate listener, not that later person.", p.Name, strings.Join(p.Aliases, ", "))
	}
	return map[string]any{
		"target": map[string]any{"type": "choice", "instructions": "Who is the immediate listener of the latest message in this conversation?", "criteria": criteria},
		"bare":   map[string]any{"type": "noul", "instructions": "Is the user's latest message only a greeting, name, or request to switch listeners, without a substantive question or task?"},
	}
}

func (c candidate) decide(ctx context.Context, req brain.AddressRequest) (brain.Addressee, bool, json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, c.budget)
	defer cancel()
	body, err := json.Marshal(map[string]any{"model": c.model, "state": req, "questions": questions(req)})
	if err != nil {
		return brain.Addressee{}, false, nil, err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return brain.Addressee{}, false, nil, err
	}
	r.Header.Set("Content-Type", "application/json")
	if c.key != "" {
		r.Header.Set("Authorization", "Bearer "+c.key)
	}
	resp, err := c.client.Do(r)
	if err != nil {
		return brain.Addressee{}, false, nil, fmt.Errorf("decision request: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return brain.Addressee{}, false, nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return brain.Addressee{}, false, nil, fmt.Errorf("decision HTTP %d", resp.StatusCode)
	}
	var result systemOneResponse
	if err = json.Unmarshal(raw, &result); err != nil {
		return brain.Addressee{}, false, nil, fmt.Errorf("decode decision: %w", err)
	}
	d, accept, err := interpret(req, result)
	return d, accept, raw, err
}

func probability(p float64) bool { return !math.IsNaN(p) && !math.IsInf(p, 0) && p >= 0 && p <= 1 }

func interpret(req brain.AddressRequest, r systemOneResponse) (brain.Addressee, bool, error) {
	b, ok := r.Answers["bare"]
	if !ok || b.Noul == nil || !probability(*b.Noul) {
		return brain.Addressee{}, false, errors.New("missing/invalid bare")
	}
	t, ok := r.Answers["target"]
	if !ok || !probability(t.Confidence) {
		return brain.Addressee{}, false, errors.New("missing/invalid target")
	}
	options := questions(req)["target"].(map[string]any)["criteria"].(map[string]string)
	if _, ok := options[t.Choice]; !ok {
		return brain.Addressee{}, false, errors.New("unknown choice")
	}
	if len(t.Probabilities) != len(options) {
		return brain.Addressee{}, false, errors.New("incomplete probability map")
	}
	sum, other := 0.0, 0.0
	for k := range options {
		p, ok := t.Probabilities[k]
		if !ok || !probability(p) {
			return brain.Addressee{}, false, errors.New("invalid probabilities")
		}
		sum += p
		if k != t.Choice {
			other = max(other, p)
		}
	}
	if math.Abs(sum-1) > 0.02 {
		return brain.Addressee{}, false, errors.New("probabilities do not sum to one")
	}
	accept := t.Confidence >= 0.85 && t.Probabilities[t.Choice]-other >= 0.20
	d := brain.Addressee{Kind: t.Choice}
	switch t.Choice {
	case "ambiguous", "unavailable":
		return d, false, nil
	case "none":
		return d, accept, nil
	}
	for i, p := range req.Participants {
		if t.Choice != fmt.Sprintf("person_%d", i) {
			continue
		}
		d.Kind, d.Name = "address", p.Name
		b, ok := r.Answers["bare"]
		if !ok || b.Noul == nil || !probability(*b.Noul) {
			return brain.Addressee{}, false, errors.New("missing/invalid bare")
		}
		if req.Account == "user" {
			d.Bare = *b.Noul >= 0.5
			accept = accept && (*b.Noul >= 0.9 || *b.Noul <= 0.1)
		}
		return d, accept && hostDecision(d, req).Kind == "address", nil
	}
	return brain.Addressee{}, false, errors.New("unmapped choice")
}

type resolveFunc func(context.Context, brain.AddressRequest) (brain.Addressee, error)

func optionalResolve(ctx context.Context, c *candidate, fallback resolveFunc, req brain.AddressRequest) (brain.Addressee, bool, json.RawMessage, string, error) {
	if err := ctx.Err(); err != nil {
		return brain.Addressee{}, false, nil, "", err
	}
	if c == nil {
		d, err := fallback(ctx, req)
		return d, false, nil, "off", err
	}
	d, accept, raw, decisionErr := c.decide(ctx, req)
	reason := "abstain"
	if decisionErr != nil {
		reason = decisionErr.Error()
	}
	if err := ctx.Err(); err != nil {
		return brain.Addressee{}, false, raw, reason, err
	}
	if accept {
		return d, false, raw, "", nil
	}
	d, err := fallback(ctx, req)
	return d, true, raw, reason, err
}

func hostDecision(d brain.Addressee, req brain.AddressRequest) brain.Addressee {
	if d.Kind != "address" {
		return d
	}
	identities := make([]summon.Identity, len(req.Participants))
	for i, p := range req.Participants {
		identities[i] = summon.Identity{ID: fmt.Sprintf("person_%d", i), DisplayName: p.Name, Aliases: p.Aliases}
	}
	roster, err := summon.NewRoster(identities)
	if err != nil {
		return brain.Addressee{Kind: "invalid"}
	}
	a, err := roster.Lookup(d.Name)
	var unknown *summon.UnknownTargetError
	var ambiguous *summon.AmbiguousTargetError
	if errors.As(err, &unknown) {
		return brain.Addressee{Kind: "unavailable", Name: d.Name}
	}
	if errors.As(err, &ambiguous) {
		return brain.Addressee{Kind: "ambiguous"}
	}
	if err != nil {
		return brain.Addressee{Kind: "invalid"}
	}
	for _, identity := range identities {
		if identity.ID == a.PersonaID {
			d.Name = identity.DisplayName
		}
	}
	return d
}

func equivalent(got, want brain.Addressee, req brain.AddressRequest) bool {
	got = hostDecision(got, req)
	return got.Kind == want.Kind && (got.Kind != "address" || (got.Name == want.Name && got.Bare == want.Bare))
}

func rescore(path string, cases []replayCase) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	byID := map[string]replayCase{}
	for _, c := range cases {
		byID[c.ID] = c
	}
	dec := json.NewDecoder(f)
	enc := json.NewEncoder(os.Stdout)
	for {
		var s sample
		if err := dec.Decode(&s); err == io.EOF {
			return nil
		} else if err != nil {
			return err
		}
		if s.ID == "" || s.Rep < 0 {
			continue
		}
		c, ok := byID[s.ID]
		if !ok {
			return fmt.Errorf("unknown case %s", s.ID)
		}
		s.Correct = s.Error == "" && equivalent(s.Decision, c.Want, c.Request)
		if err := enc.Encode(s); err != nil {
			return err
		}
	}
}

type sample struct {
	ID             string          `json:"id"`
	Rep            int             `json:"rep"`
	MS             float64         `json:"elapsed_ms"`
	Decision       brain.Addressee `json:"decision"`
	Correct        bool            `json:"correct"`
	Accepted       bool            `json:"accepted"`
	Fallback       bool            `json:"fallback"`
	FallbackReason string          `json:"fallback_reason,omitempty"`
	Raw            json.RawMessage `json:"raw,omitempty"`
	Error          string          `json:"error,omitempty"`
}

func run() error {
	casePath := flag.String("cases", "", "labeled replay JSON")
	scorePath := flag.String("rescore", "", "rescore retained JSONL through production roster validation; no model calls")
	out := flag.String("out", "", "new JSONL output path")
	mode := flag.String("mode", "baseline", "baseline|raw|hybrid")
	endpoint := flag.String("endpoint", "http://127.0.0.1:18763/v1/systemone", "decision endpoint")
	model := flag.String("model", "decider", "decision model name")
	keyEnv := flag.String("key-env", "", "environment variable for decision API key")
	brainModel := flag.String("brain-model", "qwen3.8:latest", "baseline Ollama model")
	host := flag.String("ollama-host", "http://127.0.0.1:11434", "Ollama host")
	reps := flag.Int("reps", 3, "complete replay passes")
	budget := flag.Duration("budget", 750*time.Millisecond, "optional decision budget")
	flag.Parse()
	if *casePath == "" || (*out == "" && *scorePath == "") || *reps < 1 {
		return errors.New("cases, out, and positive reps required")
	}
	if *mode != "baseline" && *mode != "raw" && *mode != "hybrid" {
		return errors.New("unknown mode")
	}
	data, err := os.ReadFile(*casePath)
	if err != nil {
		return err
	}
	var cases []replayCase
	if err = json.Unmarshal(data, &cases); err != nil {
		return err
	}
	if len(cases) == 0 {
		return errors.New("empty cases")
	}
	if *scorePath != "" {
		return rescore(*scorePath, cases)
	}
	f, err := os.OpenFile(*out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var fallback resolveFunc
	if *mode != "raw" {
		o, err := brain.NewOllama(&config.Config{BrainProvider: "ollama", OllamaModel: *brainModel, OllamaHost: *host, OllamaNumCtx: 32768, OllamaNumBatch: 128, OllamaKeepAlive: "20m", Persona: "samantha"})
		if err != nil {
			return err
		}
		fallback = o.ResolveAddressee
	}
	c := &candidate{endpoint: *endpoint, model: *model, budget: *budget, client: &http.Client{}}
	if *keyEnv != "" {
		c.key = os.Getenv(*keyEnv)
		if c.key == "" {
			return fmt.Errorf("missing %s", *keyEnv)
		}
	}
	if err = enc.Encode(map[string]any{"type": "manifest", "created_at": time.Now().UTC(), "mode": *mode, "endpoint": *endpoint, "model": *model, "brain_model": *brainModel, "budget_ms": budget.Milliseconds(), "reps": *reps, "num_ctx": 32768, "num_batch": 128, "cases": len(cases)}); err != nil {
		return err
	}
	var latencies []float64
	correct := 0
	for rep := -1; rep < *reps; rep++ {
		population := cases
		if rep == -1 {
			population = []replayCase{{ID: "warmup", Request: brain.AddressRequest{Participants: []brain.GroupParticipant{{Name: "Jarvis"}, {Name: "Santa"}}, Current: "Santa", Speaker: "Lance", Account: "user", Text: "Good evening Jarvis"}, Want: brain.Addressee{Kind: "address", Name: "Jarvis", Bare: true}}}
		}
		for i := range population {
			tc := population[(i+max(rep, 0))%len(population)]
			tctx, cancel := context.WithTimeout(ctx, 16*time.Second)
			start := time.Now()
			s := sample{ID: tc.ID, Rep: rep}
			switch *mode {
			case "baseline":
				s.Decision, err = fallback(tctx, tc.Request)
			case "raw":
				s.Decision, s.Accepted, s.Raw, err = c.decide(tctx, tc.Request)
			case "hybrid":
				s.Decision, s.Fallback, s.Raw, s.FallbackReason, err = optionalResolve(tctx, c, fallback, tc.Request)
				s.Accepted = !s.Fallback && err == nil
			}
			s.MS = float64(time.Since(start).Microseconds()) / 1000
			cancel()
			if err != nil {
				s.Error = err.Error()
			}
			s.Correct = err == nil && equivalent(s.Decision, tc.Want, tc.Request)
			if err = enc.Encode(s); err != nil {
				return err
			}
			if err = f.Sync(); err != nil {
				return err
			}
			if rep >= 0 {
				latencies = append(latencies, s.MS)
				if s.Correct {
					correct++
				}
			}
			fmt.Printf("%s rep=%d %.1fms correct=%v fallback=%v error=%s\n", s.ID, rep, s.MS, s.Correct, s.Fallback, s.Error)
			if ctx.Err() != nil {
				return ctx.Err()
			}
		}
	}
	sort.Float64s(latencies)
	fmt.Printf("SUMMARY correct=%d/%d median_ms=%.1f p95_ms=%.1f\n", correct, len(latencies), latencies[len(latencies)/2], latencies[int(math.Ceil(float64(len(latencies))*.95))-1])
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
