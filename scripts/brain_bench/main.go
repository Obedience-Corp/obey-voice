// Command brain_bench measures conversational latency of local Ollama models
// through obey-voice's real brain seam.
//
// It does not talk to the Ollama HTTP API directly. It builds a *brain.OllamaBrain
// from the live config with a persona profile applied, and drives ThinkStream —
// so every measured request carries the same system prompt (persona +
// environment + skills catalog + turn instruction), the same tool schemas, and
// the same num_ctx / think / keep_alive options a real voice turn carries.
//
// Criteria: docs/benchmarks/B1-brain-latency-ab-criteria.md (written first).
//
// The machine it runs on is a live workstation with a live Ollama server, so it
// serializes everything, stops the previous model before loading the next, reads
// ~/.obey without writing it, pulls and deletes nothing, and re-warms the
// incumbent when it finishes.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/ollama/ollama/api"

	"github.com/Obedience-Corp/obey-voice/internal/persona"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/brain"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/prompts"
)

// defaultCandidates are the models measured when -models is not given. The
// inclusion and exclusion reasoning lives in the criteria doc, not here.
var defaultCandidates = []string{
	"qwen3.8-uncensored:27b", // incumbent: live ollama_model, veronica persona
	"qwen3.8:latest",         // today's pin for the obey and samantha personas
	"qwen3:30b",              // MoE 30B-A3B
	"kimi-k2.6-distilled:IQ4_XS",
	"gemma4:26b",
	"qwen3:8b",
	"dolphin-mistral:latest",
	"llama3.2:latest",
	"llama3.3:latest", // dense 70B, run last: largest footprint
}

// benchPrompt is one conversational turn under test.
type benchPrompt struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// benchPrompts are representative of what is actually said to a voice agent:
// a short factual lookup, a short command-ish ask, a brief explanation, a
// multi-sentence reasoning request, and a short opinion. None of them invite
// tool use — tools stay in the request for prompt fidelity, but a benchmark on
// a live workstation has no business provoking run_command.
var benchPrompts = []benchPrompt{
	{ID: "P1", Kind: "factual_short", Text: "What's the capital of Australia?"},
	{ID: "P2", Kind: "command_short", Text: "In one sentence, tell me what you're good at."},
	{ID: "P3", Kind: "explain_brief", Text: "Explain briefly what a vector database is."},
	{ID: "P4", Kind: "reason_multi", Text: "I've got two hours before a meeting and three tasks: prep slides, review a pull request, and reply to email. How should I order them, and why?"},
	{ID: "P5", Kind: "opinion_short", Text: "Should I use a local model or a cloud model for voice? Keep it short."},
}

type sample struct {
	Prompt  string  `json:"prompt"`
	Rep     int     `json:"rep"`
	RouteMS float64 `json:"route_ms"`
	TTFTMS  float64 `json:"ttft_ms"`
	TotalMS float64 `json:"total_ms"`
	// PromptTokens is the FIRST model request's prompt_eval_count — the real
	// tokenized prompt, straight from the server. A tool loop issues more
	// requests; those are counted in Requests and summed into PromptTokensTotal,
	// but only the first one describes the prompt the turn started from.
	PromptTokens      int     `json:"prompt_tokens"`
	PromptTokensTotal int     `json:"prompt_tokens_all_requests"`
	GenTokens         int     `json:"gen_tokens"`
	Requests          int     `json:"model_requests"`
	GenTokS           float64 `json:"gen_tok_s"`
	PrefillTokS       float64 `json:"prefill_tok_s"`
	// SkillsActivated is the semantic router splicing skill instructions onto
	// the user message. ToolCalls is the model actually invoking a tool. They
	// arrive through the same callback and must not be conflated: one is a
	// prompt-size effect, the other is an extra round trip.
	SkillsActivated int      `json:"skills_activated"`
	ToolCalls       int      `json:"tool_calls"`
	ToolNames       []string `json:"tool_names,omitempty"`
	Chars           int      `json:"reply_chars"`
	Reply           string   `json:"reply"`
	Err             string   `json:"error,omitempty"`
}

type summary struct {
	N             int     `json:"n"`
	Errors        int     `json:"errors"`
	TTFTp50MS     float64 `json:"ttft_p50_ms"`
	TTFTp95MS     float64 `json:"ttft_p95_ms"`
	TTFTMaxMS     float64 `json:"ttft_max_ms"`
	TotalMedianMS float64 `json:"total_median_ms"`
	TotalMaxMS    float64 `json:"total_max_ms"`
	GenTokSMedian float64 `json:"gen_tok_s_median"`
	PrefillTokS   float64 `json:"prefill_tok_s_median"`
	// PromptTokens is the median REAL tokenized prompt (server prompt_eval_count
	// on the first request), which is the number that says how full num_ctx
	// actually is. The brain's own byte/4 estimate is reported separately.
	PromptTokens      int     `json:"prompt_tokens_median_real"`
	PromptTokensMax   int     `json:"prompt_tokens_max_real"`
	ToolCalls         int     `json:"tool_calls_total"`
	SkillActivations  int     `json:"skill_activations_total"`
	MultiRequestTurns int     `json:"turns_with_more_than_one_model_request"`
	RouteMedianMS     float64 `json:"route_ms_median"`
}

type gateResult struct {
	G1 bool `json:"g1_ttft_p50_le_2000ms"`
	G2 bool `json:"g2_ttft_p95_le_4000ms"`
	G3 bool `json:"g3_gen_tok_s_ge_25"`
	G4 bool `json:"g4_worst_turn_le_90s"`
}

func (g gateResult) pass() bool { return g.G1 && g.G2 && g.G3 && g.G4 }

type conditionResult struct {
	EstimatedPromptTokens int     `json:"estimated_prompt_tokens_at_start"`
	HistoryTurns          int     `json:"history_turns"`
	Summary               summary `json:"summary"`
	// CleanSummary covers only turns where the model made no tool call. The gate
	// runs on Summary, because a turn that detoured through web_search is a turn
	// the user actually waited through. CleanSummary exists so the model-to-model
	// comparison can be read without an external HTTP call in the middle of it —
	// a model that never fires a tool has identical Summary and CleanSummary.
	CleanSummary summary    `json:"clean_turns_summary"`
	CleanTurns   int        `json:"clean_turns"`
	Gate         gateResult `json:"gate"`
	GatePass     bool       `json:"gate_pass"`
	Samples      []sample   `json:"samples"`
}

type coldResult struct {
	TTFTMS  float64 `json:"ttft_ms"`
	TotalMS float64 `json:"total_ms"`
	Err     string  `json:"error,omitempty"`
}

type modelResult struct {
	Model       string                      `json:"model"`
	SizeBytes   int64                       `json:"size_bytes"`
	Digest      string                      `json:"digest"`
	Cold        coldResult                  `json:"cold_first_turn"`
	Conditions  map[string]*conditionResult `json:"conditions"`
	OverallPass bool                        `json:"overall_gate_pass"`
	SetupErr    string                      `json:"setup_error,omitempty"`
}

type artifact struct {
	ID           string           `json:"id"`
	WorkItem     string           `json:"work_item"`
	Title        string           `json:"title"`
	CriteriaDoc  string           `json:"criteria_doc"`
	GeneratedAt  string           `json:"generated_at"`
	Harness      map[string]any   `json:"harness"`
	Provenance   map[string]any   `json:"provenance"`
	Config       map[string]any   `json:"config_layering"`
	Request      map[string]any   `json:"effective_request_options"`
	SystemPrompt map[string]any   `json:"system_prompt"`
	Gate         map[string]any   `json:"gate"`
	Prompts      []benchPrompt    `json:"prompts"`
	History      map[string]any   `json:"filled_history"`
	Installed    []map[string]any `json:"installed_models"`
	Results      []*modelResult   `json:"results"`
	Notes        []string         `json:"notes"`
}

func main() {
	var (
		personaID   = flag.String("persona", "obey", "persona profile whose stack and prompts are applied")
		modelsCSV   = flag.String("models", "", "comma-separated models (default: the nine criteria candidates)")
		reps        = flag.Int("reps", 3, "measured warm repetitions per prompt per condition")
		targetTok   = flag.Int("history-tokens", 6000, "estimated prompt tokens for the filled-history condition")
		turnTimeout = flag.Duration("turn-timeout", 3*time.Minute, "per-turn deadline; mirrors pipeline defaultBrainTurnTimeout")
		workDir     = flag.String("workdir", "", "agent working directory (default: current directory)")
		out         = flag.String("out", "", "artifact path (default docs/benchmarks/B1-brain-latency-ab-<date>.json)")
		rewarm      = flag.String("rewarm", "qwen3.8-uncensored:27b", "model to leave resident when the run ends; empty to skip")
		stopBetween = flag.Bool("stop-between", true, "unload each model before measuring it, so cold-load numbers are real")
		toolsFlag   = flag.String("tools", "config", "tool schemas in the request: config|on|off")
	)
	flag.Parse()

	if err := run(*personaID, *modelsCSV, *reps, *targetTok, *turnTimeout, *workDir, *out, *rewarm, *stopBetween, *toolsFlag); err != nil {
		fmt.Fprintf(os.Stderr, "brain_bench: %v\n", err)
		os.Exit(1)
	}
}

func run(personaID, modelsCSV string, reps, targetTok int, turnTimeout time.Duration, workDirFlag, outPath, rewarm string, stopBetween bool, toolsFlag string) error {
	ctx := context.Background()

	if workDirFlag == "" {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("resolving working directory: %w", err)
		}
		workDirFlag = wd
	}

	// LoadRaw, not Load: Load runs the persona afterLoad hook, which is allowed
	// to write migration files. This benchmark reads ~/.obey and writes nothing,
	// so the persona overlay is applied explicitly below instead — same call the
	// hook makes, minus the ability to mutate disk.
	base, err := config.LoadRaw()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	profile, err := persona.Load(personaID)
	if err != nil {
		return fmt.Errorf("loading persona %q: %w", personaID, err)
	}

	models := defaultCandidates
	if strings.TrimSpace(modelsCSV) != "" {
		models = splitCSV(modelsCSV)
	}

	// A separate client for server-level operations (list, unload). The brain
	// builds its own for chat.
	baseURL, err := url.Parse(base.OllamaHost)
	if err != nil {
		return fmt.Errorf("invalid ollama_host %q: %w", base.OllamaHost, err)
	}
	admin := api.NewClient(baseURL, &http.Client{Timeout: 60 * time.Second})
	if err := admin.Heartbeat(ctx); err != nil {
		return fmt.Errorf("ollama not reachable at %s: %w", base.OllamaHost, err)
	}
	ollamaVersion, _ := admin.Version(ctx)
	installed, err := admin.List(ctx)
	if err != nil {
		return fmt.Errorf("listing installed models: %w", err)
	}
	byName := map[string]api.ListModelResponse{}
	var installedRows []map[string]any
	for _, m := range installed.Models {
		byName[m.Name] = m
		installedRows = append(installedRows, map[string]any{
			"name": m.Name, "size_bytes": m.Size, "digest": shortDigest(m.Digest), "modified": m.ModifiedAt.Format(time.RFC3339),
		})
	}

	// Build one brain up front, purely to read the assembled system prompt and
	// size the filled history against it. It uses the first candidate's model
	// only because NewOllama validates the model exists; the prompt is identical
	// across candidates.
	probeCfg := configFor(base, profile, workDirFlag, models[0])
	probe, err := brain.NewOllama(probeCfg)
	if err != nil {
		return fmt.Errorf("building probe brain: %w", err)
	}
	emptyPromptTokens := probe.SessionInfo().PromptTokens
	sysPrompt, err := renderSystemPrompt(ctx, probeCfg)
	if err != nil {
		return err
	}
	// Cross-check: estimateTokens with no history is len(fullSystemPrompt)/4, so
	// a rendered prompt that agrees within a token is the same prompt. A mismatch
	// means the render drifted from what the brain assembled, and the artifact
	// should say so rather than quietly report the wrong prompt.
	promptRenderAgrees := abs(len(sysPrompt)/4-emptyPromptTokens) <= 1

	filled := buildFilledHistory(targetTok, func(turns []brain.Turn) int {
		probe.LoadHistory(turns)
		n := probe.SessionInfo().PromptTokens
		probe.ClearHistory()
		return n
	})
	probe.LoadHistory(filled)
	filledPromptTokens := probe.SessionInfo().PromptTokens
	probe.ClearHistory()

	toolsEnabled := probeCfg.VoiceToolsEnabled
	switch strings.ToLower(toolsFlag) {
	case "on":
		toolsEnabled = true
	case "off":
		toolsEnabled = false
	}

	trimLine := probeCfg.OllamaNumCtx * 9 / 10
	if filledPromptTokens > trimLine {
		return fmt.Errorf("filled history estimate %d exceeds the ensureContextBudget trim line %d — lower -history-tokens", filledPromptTokens, trimLine)
	}

	fmt.Fprintf(os.Stderr, "brain_bench: persona=%s workdir=%s tools=%v num_ctx=%d think=%q keep_alive=%s\n",
		personaID, workDirFlag, toolsEnabled, probeCfg.OllamaNumCtx, probeCfg.OllamaThink, probeCfg.OllamaKeepAlive)
	fmt.Fprintf(os.Stderr, "brain_bench: system prompt %d bytes (~%d tokens); filled history %d turns (~%d tokens, trim line %d)\n",
		len(sysPrompt), emptyPromptTokens, len(filled), filledPromptTokens, trimLine)

	art := &artifact{
		ID:          "B1",
		WorkItem:    "WI-a981e4",
		Title:       "Conversational brain model A/B through the real obey-voice brain seam",
		CriteriaDoc: "docs/benchmarks/B1-brain-latency-ab-criteria.md",
		GeneratedAt: time.Now().Format(time.RFC3339),
		Harness: map[string]any{
			"command":      "go run ./scripts/brain_bench",
			"path":         "scripts/brain_bench",
			"seam":         "brain.NewOllama(cfg).ThinkStream(ctx, input, StreamOptions{VoiceMode:true, ToolsEnabled:...})",
			"reps":         reps,
			"prompts":      len(benchPrompts),
			"turn_timeout": turnTimeout.String(),
			"stop_between": stopBetween,
			"persona":      personaID,
			"workdir":      workDirFlag,
		},
		Provenance: provenance(ollamaVersion),
		Config: map[string]any{
			"source":                   config.ConfigFile(),
			"config_brain_provider":    base.BrainProvider,
			"config_ollama_model":      base.OllamaModel,
			"config_active_persona":    base.ActivePersona,
			"persona_measured":         personaID,
			"persona_brain_provider":   profile.Brain.Provider,
			"persona_brain_model":      profile.Brain.Model,
			"effective_brain_provider": probeCfg.BrainProvider,
			"note":                     "config brain_provider is grok, but every persona profile pins provider ollama, so the persona overlay decides. The ollama path is what the live session logs show, and it is what is measured here.",
		},
		Request: map[string]any{
			"num_ctx":            probeCfg.OllamaNumCtx,
			"think":              probeCfg.OllamaThink,
			"keep_alive":         probeCfg.OllamaKeepAlive,
			"stream":             true,
			"tools_enabled":      toolsEnabled,
			"voice_tools_config": probeCfg.VoiceToolsEnabled,
			"skills_enabled":     probeCfg.SkillsEnabled,
			"embedding_model":    probeCfg.OllamaEmbeddingModel,
			"max_history":        probeCfg.MaxHistory,
			"agent_name":         probeCfg.AgentName,
			"persona_prompt":     probeCfg.Persona,
			"turn_prompt":        probeCfg.TurnPrompt,
		},
		SystemPrompt: map[string]any{
			"bytes":                len(sysPrompt),
			"sha256":               sha256hex(sysPrompt),
			"estimated_tokens":     emptyPromptTokens,
			"render_matches_brain": promptRenderAgrees,
			"elements":             "persona + environment + skills catalog + turn instruction (brain.AssembleSystemPrompt, ollama policy)",
		},
		Gate: map[string]any{
			"g1_ttft_p50_ms":           2000,
			"g2_ttft_p95_ms":           4000,
			"g3_gen_tok_s":             25,
			"g4_worst_turn_ms":         90000,
			"g5":                       "G1-G4 must hold in both history conditions",
			"pipeline_turn_timeout_ms": 180000,
			"p95_caveat":               "nearest-rank on n=15; read as worst observed, not a distributional claim",
		},
		Prompts: benchPrompts,
		History: map[string]any{
			"turns":                   len(filled),
			"estimated_prompt_tokens": filledPromptTokens,
			"target_tokens":           targetTok,
			"trim_line_tokens":        trimLine,
			"source":                  "scripts/brain_bench/history.go — real dialogue about this repository's work, tail-trimmed to fit",
		},
		Installed: installedRows,
		Notes: []string{
			"Latency only. Nothing here judges response quality; that is the user's judgment in real use.",
			"Each sample resets history to the condition baseline first, so prompt size is constant across reps.",
			"route_ms is the synchronous skill-router embedding call and is included in ttft_ms, because the user waits for it.",
			"~/.obey was read, never written. No model was pulled or deleted. Models were unloaded (not removed) to measure cold load.",
		},
	}

	for _, model := range models {
		res := &modelResult{Model: model, Conditions: map[string]*conditionResult{}}
		if m, ok := byName[model]; ok {
			res.SizeBytes, res.Digest = m.Size, shortDigest(m.Digest)
		}
		art.Results = append(art.Results, res)

		if _, ok := byName[model]; !ok {
			res.SetupErr = "not installed"
			fmt.Fprintf(os.Stderr, "brain_bench: skipping %s — not installed\n", model)
			continue
		}

		fmt.Fprintf(os.Stderr, "\nbrain_bench: === %s ===\n", model)
		if stopBetween {
			unloadAll(ctx, admin, models)
		}

		cfg := configFor(base, profile, workDirFlag, model)
		b, err := brain.NewOllama(cfg)
		if err != nil {
			res.SetupErr = err.Error()
			fmt.Fprintf(os.Stderr, "brain_bench: %s setup failed: %v\n", model, err)
			continue
		}

		// Cold: first real turn after an unload. Reported, not gated.
		b.ClearHistory()
		cold := measure(ctx, b, benchPrompts[0], toolsEnabled, turnTimeout, 0)
		res.Cold = coldResult{TTFTMS: cold.TTFTMS, TotalMS: cold.TotalMS, Err: cold.Err}
		fmt.Fprintf(os.Stderr, "  cold  ttft=%.0fms total=%.0fms %s\n", cold.TTFTMS, cold.TotalMS, cold.Err)

		for _, cond := range []struct {
			name string
			base []brain.Turn
			est  int
		}{
			{"empty_history", nil, emptyPromptTokens},
			{"filled_history_6k", filled, filledPromptTokens},
		} {
			cr := &conditionResult{EstimatedPromptTokens: cond.est, HistoryTurns: len(cond.base)}
			res.Conditions[cond.name] = cr

			// One discarded turn per condition. Without it exactly one measured
			// sample — the first — pays an uncached prefill of the whole history
			// while every later sample reuses the server's KV prefix cache, and
			// that lone outlier lands squarely on the p95. A real conversation
			// keeps its prefix cached and re-reads only the new turn, so the
			// cached state is the honest one to measure; the discard is how the
			// benchmark reaches it instead of straddling both.
			b.ClearHistory()
			if len(cond.base) > 0 {
				b.LoadHistory(cond.base)
			}
			warm := measure(ctx, b, benchPrompts[0], toolsEnabled, turnTimeout, 0)
			fmt.Fprintf(os.Stderr, "  %-18s warm-up (discarded) ttft=%.0fms prompt=%d %s\n", cond.name, warm.TTFTMS, warm.PromptTokens, warm.Err)

			for rep := 1; rep <= reps; rep++ {
				for _, p := range benchPrompts {
					b.ClearHistory()
					if len(cond.base) > 0 {
						b.LoadHistory(cond.base)
					}
					s := measure(ctx, b, p, toolsEnabled, turnTimeout, rep)
					cr.Samples = append(cr.Samples, s)
					fmt.Fprintf(os.Stderr, "  %-18s %s r%d ttft=%.0fms total=%.0fms gen=%.1f tok/s prompt=%d %s\n",
						cond.name, p.ID, rep, s.TTFTMS, s.TotalMS, s.GenTokS, s.PromptTokens, s.Err)
				}
			}
			cr.Summary = summarize(cr.Samples)
			var clean []sample
			for _, s := range cr.Samples {
				if s.ToolCalls == 0 {
					clean = append(clean, s)
				}
			}
			cr.CleanTurns = len(clean)
			cr.CleanSummary = summarize(clean)
			cr.Gate = evaluate(cr.Summary)
			cr.GatePass = cr.Gate.pass() && cr.Summary.Errors == 0
		}

		res.OverallPass = res.Conditions["empty_history"].GatePass && res.Conditions["filled_history_6k"].GatePass
		fmt.Fprintf(os.Stderr, "  verdict: %s\n", passWord(res.OverallPass))
	}

	if strings.TrimSpace(rewarm) != "" {
		fmt.Fprintf(os.Stderr, "\nbrain_bench: re-warming incumbent %s\n", rewarm)
		unloadAll(ctx, admin, models)
		cfg := configFor(base, profile, workDirFlag, rewarm)
		if b, err := brain.NewOllama(cfg); err == nil {
			wctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			b.Warmup(wctx)
			cancel()
			if running, err := admin.ListRunning(ctx); err == nil {
				for _, m := range running.Models {
					fmt.Fprintf(os.Stderr, "brain_bench: resident: %s until %s\n", m.Name, m.ExpiresAt.Format(time.RFC3339))
					art.Notes = append(art.Notes, fmt.Sprintf("left resident after the run: %s (expires %s)", m.Name, m.ExpiresAt.Format(time.RFC3339)))
				}
			}
		} else {
			fmt.Fprintf(os.Stderr, "brain_bench: re-warm failed: %v\n", err)
		}
	}

	if outPath == "" {
		outPath = fmt.Sprintf("docs/benchmarks/B1-brain-latency-ab-%s.json", time.Now().Format("2006-01-02"))
	}
	blob, err := json.MarshalIndent(art, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding artifact: %w", err)
	}
	if err := os.WriteFile(outPath, append(blob, '\n'), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", outPath, err)
	}
	fmt.Fprintf(os.Stderr, "\nbrain_bench: wrote %s\n", outPath)
	printTable(art)
	return nil
}

// measure drives one turn through ThinkStream and times it from the caller's
// side: the clock starts before the skill router runs, because the user is
// already waiting by then.
func measure(ctx context.Context, b *brain.OllamaBrain, p benchPrompt, tools bool, timeout time.Duration, rep int) sample {
	s := sample{Prompt: p.ID, Rep: rep}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var prefillFirst, prefillAll, gen, requests, toolCalls, skills int
	var toolNames []string
	start := time.Now()
	stream, err := b.ThinkStream(tctx, p.Text, brain.StreamOptions{
		VoiceMode:    true,
		ToolsEnabled: tools,
		OnUsage: func(pf, g int) {
			if requests == 0 {
				prefillFirst = pf
			}
			requests++
			prefillAll += pf
			gen += g
		},
		OnToolStart: func(name, detail string) {
			if name == "activate_skill" {
				skills++
				return
			}
			toolCalls++
			toolNames = append(toolNames, name+"("+truncate(detail, 120)+")")
		},
	})
	s.RouteMS = msSince(start)
	if err != nil {
		s.Err = err.Error()
		s.TTFTMS, s.TotalMS = msSince(start), msSince(start)
		return s
	}

	var reply strings.Builder
	first := time.Time{}
	for chunk := range stream.Chunks {
		if first.IsZero() {
			first = time.Now()
			s.TTFTMS = float64(first.Sub(start).Microseconds()) / 1000
		}
		reply.WriteString(chunk)
	}
	result := <-stream.Done
	s.TotalMS = msSince(start)
	if first.IsZero() {
		s.TTFTMS = s.TotalMS
	}
	if result.Err != nil {
		s.Err = result.Err.Error()
	}
	s.PromptTokens, s.PromptTokensTotal, s.GenTokens = prefillFirst, prefillAll, gen
	s.Requests, s.ToolCalls, s.SkillsActivated = requests, toolCalls, skills
	s.ToolNames = toolNames
	s.Reply = strings.TrimSpace(reply.String())
	s.Chars = len(s.Reply)
	if genMS := s.TotalMS - s.TTFTMS; genMS > 0 && gen > 0 {
		s.GenTokS = float64(gen) / (genMS / 1000)
	}
	if s.TTFTMS > 0 && prefillFirst > 0 {
		s.PrefillTokS = float64(prefillFirst) / (s.TTFTMS / 1000)
	}
	return s
}

func summarize(samples []sample) summary {
	sum := summary{N: len(samples)}
	var ttft, total, gen, prefill, route []float64
	var prompt []float64
	for _, s := range samples {
		if s.Err != "" {
			sum.Errors++
		}
		sum.ToolCalls += s.ToolCalls
		sum.SkillActivations += s.SkillsActivated
		if s.Requests > 1 {
			sum.MultiRequestTurns++
		}
		ttft = append(ttft, s.TTFTMS)
		total = append(total, s.TotalMS)
		route = append(route, s.RouteMS)
		prompt = append(prompt, float64(s.PromptTokens))
		if s.GenTokS > 0 {
			gen = append(gen, s.GenTokS)
		}
		if s.PrefillTokS > 0 {
			prefill = append(prefill, s.PrefillTokS)
		}
	}
	sum.TTFTp50MS = pct(ttft, 50)
	sum.TTFTp95MS = pct(ttft, 95)
	sum.TTFTMaxMS = pct(ttft, 100)
	sum.TotalMedianMS = pct(total, 50)
	sum.TotalMaxMS = pct(total, 100)
	sum.GenTokSMedian = pct(gen, 50)
	sum.PrefillTokS = pct(prefill, 50)
	sum.PromptTokens = int(pct(prompt, 50))
	sum.PromptTokensMax = int(pct(prompt, 100))
	sum.RouteMedianMS = pct(route, 50)
	return sum
}

func evaluate(s summary) gateResult {
	return gateResult{
		G1: s.TTFTp50MS <= 2000,
		G2: s.TTFTp95MS <= 4000,
		G3: s.GenTokSMedian >= 25,
		G4: s.TotalMaxMS <= 90000,
	}
}

// pct is the nearest-rank percentile. With n=15 a p95 is effectively the worst
// or second-worst observation, which is exactly how the criteria doc says to
// read it.
func pct(v []float64, p int) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	rank := (p*len(s) + 99) / 100
	if rank < 1 {
		rank = 1
	}
	if rank > len(s) {
		rank = len(s)
	}
	return s[rank-1]
}

// configFor returns an in-memory copy of the live config with the persona
// overlay applied and one model substituted. The live config on disk is never
// touched.
func configFor(base *config.Config, p *persona.Profile, workDir, model string) *config.Config {
	cfg := *base
	persona.Apply(&cfg, p)
	cfg.WorkDir = workDir
	cfg.OllamaModel = model
	return &cfg
}

// renderSystemPrompt reproduces the assembled system prompt for provenance
// (byte length and hash), through brain.AssembleSystemPrompt — the same
// function the brain itself calls, reached the same way `obey-voice prompts
// show --provider ollama` reaches it. Nothing here re-implements the policy.
//
// The authoritative size still comes from the brain's own estimator
// (SessionInfo().PromptTokens); this is cross-checked against it by the caller,
// so a drift between the two would be visible rather than silent.
func renderSystemPrompt(ctx context.Context, cfg *config.Config) (string, error) {
	workDir := config.WorkDirFrom(cfg)
	userDir := cfg.PromptsDir
	if userDir == "" {
		userDir = config.PromptsDirFrom(cfg)
	}
	doc, err := (prompts.Resolver{UserDir: userDir}).Resolve(prompts.KindPersona, cfg.Persona)
	if err != nil {
		return "", fmt.Errorf("resolving persona prompt %q: %w", cfg.Persona, err)
	}
	personaText, err := prompts.ResolvePlaceholders(doc.Assemble(), prompts.PlaceholderNames(), prompts.PlaceholderValues(cfg.AgentName, prompts.PlaceholderEnv{}))
	if err != nil {
		return "", fmt.Errorf("persona prompt %q: %w", cfg.Persona, err)
	}
	catalog, err := brain.SkillsCatalogFor(ctx, cfg, workDir)
	if err != nil {
		return "", fmt.Errorf("loading skills catalog: %w", err)
	}
	turn, err := brain.TurnInstructionFor(cfg)
	if err != nil {
		return "", fmt.Errorf("resolving turn instruction: %w", err)
	}
	return brain.AssembleSystemPrompt(brain.SystemPromptInput{
		Provider: "ollama",
		Persona:  personaText,
		WorkDir:  workDir,
		Skills:   catalog,
		Turn:     turn,
		Cfg:      cfg,
	}), nil
}

// unloadAll releases every candidate from memory so the next cold-load number
// is real. This is an unload, not a delete: the model files are untouched and
// the next request loads them again.
func unloadAll(ctx context.Context, c *api.Client, models []string) {
	zero := &api.Duration{}
	for _, m := range models {
		uctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		_ = c.Generate(uctx, &api.GenerateRequest{Model: m, KeepAlive: zero}, func(api.GenerateResponse) error { return nil })
		cancel()
	}
	time.Sleep(2 * time.Second)
}

func provenance(ollamaVersion string) map[string]any {
	return map[string]any{
		"commit":         gitOut("rev-parse", "HEAD"),
		"branch":         gitOut("rev-parse", "--abbrev-ref", "HEAD"),
		"worktree_clean": gitOut("status", "--porcelain") == "",
		"machine":        sysctl("hw.model"),
		"cpu":            sysctl("machdep.cpu.brand_string"),
		"memory_bytes":   sysctl("hw.memsize"),
		"os":             strings.Join(strings.Fields(cmdOut("sw_vers", "-productVersion")), " "),
		"ollama_version": ollamaVersion,
	}
}

func printTable(a *artifact) {
	fmt.Printf("\n%-28s %8s %10s %10s %9s %9s %6s\n", "model", "cond", "ttft_p50", "ttft_p95", "tok/s", "worst_s", "gate")
	for _, r := range a.Results {
		if r.SetupErr != "" {
			fmt.Printf("%-28s %s\n", r.Model, r.SetupErr)
			continue
		}
		for _, name := range []string{"empty_history", "filled_history_6k"} {
			c := r.Conditions[name]
			if c == nil {
				continue
			}
			short := "empty"
			if name != "empty_history" {
				short = "6k"
			}
			fmt.Printf("%-28s %8s %9.0fms %9.0fms %9.1f %9.1f %6s\n",
				r.Model, short, c.Summary.TTFTp50MS, c.Summary.TTFTp95MS,
				c.Summary.GenTokSMedian, c.Summary.TotalMaxMS/1000, passWord(c.GatePass))
		}
	}
}

func passWord(ok bool) string {
	if ok {
		return "PASS"
	}
	return "FAIL"
}

func msSince(t time.Time) float64 { return float64(time.Since(t).Microseconds()) / 1000 }

func truncate(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func sha256hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func shortDigest(d string) string {
	if len(d) > 12 {
		return d[:12]
	}
	return d
}

func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func gitOut(args ...string) string { return cmdOut("git", args...) }
func sysctl(key string) string     { return cmdOut("sysctl", "-n", key) }

func cmdOut(name string, args ...string) string {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
