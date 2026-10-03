//go:build integration

package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/decider"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/decision"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/events"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/job"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/tts"
)

type liveDecisionCase struct {
	ID    string             `json:"id"`
	State decision.WorkInput `json:"state"`
	Want  struct {
		Work bool   `json:"work"`
		Tier string `json:"tier"`
	} `json:"want"`
}

type liveDecisionSample struct {
	ID             string                   `json:"id"`
	Rep            int                      `json:"rep"`
	Mode           string                   `json:"mode"`
	ModelCalls     int64                    `json:"model_calls"`
	AdmissionCalls int64                    `json:"admission_calls"`
	AdmissionMS    float64                  `json:"admission_ms"`
	FirstPCMMS     *float64                 `json:"first_pcm_ms"`
	WorkerStartMS  *float64                 `json:"worker_start_ms"`
	ElapsedMS      float64                  `json:"elapsed_ms"`
	PCMSamples     int                      `json:"pcm_samples"`
	WorkerProof    bool                     `json:"worker_marker_written"`
	Correct        bool                     `json:"correct"`
	Error          string                   `json:"error,omitempty"`
	Route          events.RouteDecided      `json:"route"`
	Decision       events.DecisionEvaluated `json:"decision"`
	Metrics        events.TurnMetrics       `json:"metrics"`
}

// This is an explicit local performance experiment, never a default test. It
// uses actual admission/ThinkStream HTTP calls, the production voice pipeline,
// Kokoro synthesis and worker admission. The controlled worker writes a marker
// in a temporary directory. The software PCM sink measures delivery of genuine
// audio frames, not physical speaker audibility or microphone/STT latency.
func TestDecisionLivePipelineReplay(t *testing.T) {
	if os.Getenv("OBEY_DECISION_LIVE") != "1" {
		t.Skip("explicit local experiment only")
	}
	modelsDir := os.Getenv("OBEY_DECISION_MODELS")
	if !filepath.IsAbs(modelsDir) {
		t.Fatal("OBEY_DECISION_MODELS must name an installed isolated model directory")
	}
	raw, err := os.ReadFile(os.Getenv("OBEY_DECISION_CASES"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []liveDecisionCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if filter := os.Getenv("OBEY_DECISION_CASE_IDS"); filter != "" {
		ids := strings.Split(filter, ",")
		cases = slices.DeleteFunc(cases, func(c liveDecisionCase) bool { return !slices.Contains(ids, c.ID) })
		if len(cases) != len(ids) {
			t.Fatal("unknown or duplicate case filter")
		}
	}
	if len(cases) == 0 {
		t.Fatal("no cases")
	}
	reps := 4
	if value := os.Getenv("OBEY_DECISION_REPS"); value != "" {
		reps, err = strconv.Atoi(value)
		if err != nil || reps < 1 {
			t.Fatal("invalid repetitions")
		}
	}
	out, err := os.OpenFile(os.Getenv("OBEY_DECISION_OUT"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	emit := func(v any) {
		if err := json.NewEncoder(out).Encode(v); err != nil {
			t.Fatal(err)
		}
		if err := out.Sync(); err != nil {
			t.Fatal(err)
		}
	}
	ctx := t.Context()
	server, err := decider.Start(ctx, modelsDir, "2b")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	evaluator, err := decision.NewSystemOne(decision.SystemOneConfig{Provider: "local", Endpoint: server.Endpoint()})
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	base, _ := url.Parse("http://127.0.0.1:11434")
	proxy := httputil.NewSingleHostReverseProxy(base)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	proxy.Transport = transport
	proxy.ModifyResponse = func(r *http.Response) error {
		if r.StatusCode >= 300 && r.StatusCode < 400 {
			return fmt.Errorf("Ollama redirect refused")
		}
		return nil
	}
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" && r.URL.Path != "/api/chat" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/api/chat" {
			calls.Add(1)
		}
		proxy.ServeHTTP(w, r)
	}))
	defer ollama.Close()
	model := os.Getenv("OBEY_DECISION_BRAIN_MODEL")
	if model == "" {
		model = "qwen3.8:latest"
	}
	cfg := tierTestConfig()
	cfg.ModelsDir, cfg.TTSProvider, cfg.TTSVoice, cfg.SpeechSpeed, cfg.Language = modelsDir, "kokoro", "af_heart", 1, "en-US"
	cfg.OllamaHost, cfg.OllamaModel, cfg.OllamaNumCtx, cfg.OllamaNumBatch, cfg.OllamaKeepAlive, cfg.OllamaThink = ollama.URL, model, 32768, 128, "20m", "false"
	cfg.WorkDir, cfg.PromptsDir, cfg.Persona, cfg.AgentName = t.TempDir(), t.TempDir(), "samantha", "Samantha"
	w := cfg.VoiceWorkers.Clone()
	cfg.VoiceWorkers.Tiers.Medium, cfg.VoiceWorkers.Tiers.Complex = w.Tiers.Simple, w.Clone().Tiers.Simple
	b, err := brain.NewOllama(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	voice, err := tts.NewKokoroTTS(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer voice.Delete()
	personaHash := b.FreezePersonaPrompt()
	questions, _ := json.Marshal(decision.RouteWorkRequest(decision.WorkInput{}).Questions)
	executable, _ := os.Executable()
	binary, _ := os.ReadFile(executable)
	emit(map[string]any{"type": "manifest", "created_at": time.Now().UTC(), "scope": "post-STT voice pipeline; real local Ollama and Kokoro; controlled worker marker; software PCM sink, no physical audibility claim", "cases_sha256": fmt.Sprintf("%x", sha256.Sum256(raw)), "question_sha256": fmt.Sprintf("%x", sha256.Sum256(questions)), "binary_sha256": fmt.Sprintf("%x", sha256.Sum256(binary)), "persona_hash": personaHash, "model": model, "decision_model_revision": "533964dae8be954c5b5e19fa4948e48408094c1e", "runtime_revision": decider.RuntimeRevision, "reps": reps, "cases": len(cases), "case_filter": os.Getenv("OBEY_DECISION_CASE_IDS"), "num_ctx": 32768, "num_batch": 128, "thinking": false, "budget_ms": 750})
	// Warm both mandatory models before recorded pairs. Failures are retained
	// and stop the run, so a cold/retry observation cannot masquerade as warm.
	warm := liveDecisionCase{ID: "warmup", State: decision.WorkInput{Text: "What is a compiler?"}}
	warmResult := liveDecisionTurn(t, ctx, b, voice, evaluator, cfg, &calls, warm, "baseline", -1)
	emit(warmResult)
	if warmResult.Error != "" || warmResult.FirstPCMMS == nil {
		t.Fatal("warmup failed; retained output")
	}
	for rep := 0; rep < reps; rep++ {
		for offset := range cases {
			i := (offset + rep) % len(cases)
			modes := []string{"baseline", "optional"}
			if (i+rep)%2 != 0 {
				slices.Reverse(modes)
			}
			for _, mode := range modes {
				emit(liveDecisionTurn(t, ctx, b, voice, evaluator, cfg, &calls, cases[i], mode, rep))
			}
		}
		t.Logf("completed pass %d/%d", rep+1, reps)
	}
}

func liveDecisionTurn(t *testing.T, parent context.Context, b *brain.OllamaBrain, voice tts.Provider, evaluator decision.Evaluator, cfg *config.Config, calls *atomic.Int64, c liveDecisionCase, mode string, rep int) liveDecisionSample {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 90*time.Second)
	defer cancel()
	var history []brain.Turn
	var conversation []brain.AddressMessage
	for _, message := range c.State.Conversation {
		history = append(history, brain.Turn{Role: message.Role, Content: message.Text})
		name, account := "User", "user"
		if message.Role == "assistant" {
			name, account = "Samantha", "agent"
		}
		conversation = append(conversation, brain.AddressMessage{Name: name, Account: account, Text: message.Text})
	}
	b.LoadHistory(history)
	result := liveDecisionSample{ID: c.ID, Mode: mode, Rep: rep}
	var mu sync.Mutex
	start := time.Now()
	before := calls.Load()
	player := &decisionPCMSink{onFrame: func(samples int) {
		mu.Lock()
		defer mu.Unlock()
		if result.FirstPCMMS == nil {
			v := float64(time.Since(start)) / float64(time.Millisecond)
			result.FirstPCMMS = &v
		}
		result.PCMSamples += samples
	}}
	defer player.Close()
	p := &Pipeline{Brain: b, STT: &fakeSTT{text: c.State.Text}, TTS: voice, Player: player, Events: events.NewBus()}
	if err := p.BeginConversationIdentity("local-decision-replay"); err != nil {
		t.Fatal(err)
	}
	jobs := job.NewTable(ctx)
	defer jobs.Close()
	a := NewAgency(p, jobs, nil, cfg, nil)
	a.Wire()
	marker := filepath.Join(t.TempDir(), "worker-started.txt")
	workerDone := make(chan struct{}, 1)
	a.planFactory = func(context.Context, *config.Config, *config.WorkerPlan) (brain.Provider, error) {
		return &decisionReplayWorker{fakeBrain: &fakeBrain{}, run: func(ctx context.Context, brief string) (string, error) {
			mu.Lock()
			at := float64(time.Since(start)) / float64(time.Millisecond)
			result.WorkerStartMS = &at
			mu.Unlock()
			if err := ctx.Err(); err != nil {
				return "", err
			}
			err := os.WriteFile(marker, []byte(brief), 0600)
			workerDone <- struct{}{}
			return "Controlled benchmark work completed.", err
		}}, nil
	}
	events.Subscribe(p.Events, func(e events.RouteDecided) { mu.Lock(); result.Route = e; mu.Unlock() })
	events.Subscribe(p.Events, func(e events.DecisionEvaluated) { mu.Lock(); result.Decision = e; mu.Unlock() })
	events.Subscribe(p.Events, func(e events.TurnMetrics) { mu.Lock(); result.Metrics = e; mu.Unlock() })
	if mode == "optional" {
		p.Decisions.Configure(decision.Options{Policy: decision.WorkPolicy{Evaluator: evaluator, Mode: decision.PolicyRouteChoice}, Budget: 750 * time.Millisecond, VoiceWork: true})
	}
	defer p.Decisions.Close()
	p.AdmitInput = func(ctx context.Context, text string, _ bool) (ResponseAdmission, error) {
		before := calls.Load()
		admitStart := time.Now()
		address, err := b.ResolveAddressee(ctx, brain.AddressRequest{Participants: []brain.GroupParticipant{{Name: "Samantha"}, {Name: "Jarvis"}}, Current: "Samantha", Speaker: "User", Account: "user", Conversation: conversation, Text: text})
		result.AdmissionMS = float64(time.Since(admitStart)) / float64(time.Millisecond)
		result.AdmissionCalls = calls.Load() - before
		if err == nil && !(address.Kind == "none" || (address.Kind == "address" && address.Name == "Samantha" && !address.Bare)) {
			err = fmt.Errorf("unexpected addressee %s %s", address.Kind, address.Name)
		}
		return ResponseAdmission{Request: text}, err
	}
	_, err := p.RunTurn(ctx)
	result.ElapsedMS = float64(time.Since(start)) / float64(time.Millisecond)
	result.ModelCalls = calls.Load() - before
	if err != nil {
		result.Error = err.Error()
	}
	mu.Lock()
	accepted := result.Route.State == "accepted"
	mu.Unlock()
	if accepted {
		select {
		case <-workerDone:
		case <-ctx.Done():
			result.Error = "worker did not complete marker: " + ctx.Err().Error()
		}
	}
	if _, err := os.Stat(marker); err == nil {
		result.WorkerProof = true
	}
	mu.Lock()
	defer mu.Unlock()
	result.Correct = result.Error == "" && accepted == c.Want.Work && (!accepted || result.Route.Tier == c.Want.Tier)
	return result
}

type decisionReplayWorker struct {
	*fakeBrain
	run func(context.Context, string) (string, error)
}

func (w *decisionReplayWorker) ThinkFull(ctx context.Context, text string, _ brain.StreamOptions) (string, error) {
	return w.run(ctx, text)
}

type decisionPCMSink struct {
	active  atomic.Int32
	onFrame func(int)
}

func (p *decisionPCMSink) SinkKind() string { return audio.SinkNone }
func (p *decisionPCMSink) PlayStream(ctx context.Context, stream *audio.PCMStream) (*audio.Playback, error) {
	started, done := make(chan struct{}), make(chan audio.PlaybackResult, 1)
	p.active.Add(1)
	go func() {
		defer p.active.Add(-1)
		defer close(done)
		rate, err := stream.WaitReady(ctx)
		n := 0
		begun := false
		for frame := range stream.Frames() {
			if err == nil && ctx.Err() == nil && len(frame) > 0 {
				if !begun {
					p.onFrame(len(frame))
					close(started)
					begun = true
				} else {
					p.onFrame(len(frame))
				}
				n += len(frame)
			}
		}
		if err == nil {
			err = stream.Err()
		}
		if err == nil {
			err = ctx.Err()
		}
		seconds := 0.0
		if rate > 0 {
			seconds = float64(n) / float64(rate)
		}
		done <- audio.PlaybackResult{Err: err, Interrupted: ctx.Err() != nil, AudioSeconds: seconds, SynthesisEndedAt: time.Now()}
	}()
	return audio.NewPlayback(started, done), nil
}
func (*decisionPCMSink) Stop()             {}
func (p *decisionPCMSink) IsPlaying() bool { return p.active.Load() > 0 }
func (*decisionPCMSink) Close() error      { return nil }

var _ io.Closer = (*decisionPCMSink)(nil)
