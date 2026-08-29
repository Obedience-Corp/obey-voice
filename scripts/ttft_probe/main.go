// Command ttft_probe answers a question B1 could not: *why* the qwen3.8 family
// pays a fixed multi-second cost before its first speakable token.
//
// B1 measured through the repo's own brain seam, which streams only
// message.content and records only two of Ollama's six counters. That seam is
// the right one for measuring the product, and the wrong one for diagnosing the
// runner underneath it: everything that would name the mechanism —
// message.thinking, prompt_eval_duration, load_duration, eval_duration — is
// dropped before it reaches the caller.
//
// So this probe talks to Ollama directly. It is a diagnostic instrument, not a
// product path, and it is deliberately self-contained: no persona, no config,
// no ~/.obey. What it measures is the runner's behaviour, which is where the
// missing seconds live.
//
// Criteria fixed in advance: docs/benchmarks/B2-qwen-ttft-mechanism-criteria.md
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ollama/ollama/api"
)

// probeNumCtx pins every request to the size the product actually runs at.
// Omitting num_ctx lets Ollama size the runner to the model's advertised window
// — 262144 on the qwen3.8 tags — which pins RAM until keep_alive expires. PR
// #62's review documented that trap; this probe does not re-step in it.
const probeNumCtx = 8192

// probeKeepAlive matches the product's ollama_keep_alive so the model stays
// resident between samples. A reload mid-run would show up as load_duration and
// contaminate the very number being measured.
var probeKeepAlive = &api.Duration{Duration: 10 * time.Minute}

// systemPrompt stands in for the assembled persona prompt. It is not the real
// one — the real assembly needs config and a persona, and reproducing it would
// couple a diagnostic to the product's prompt plumbing for no diagnostic gain.
// What matters is that the production profile lands in the same prompt-size
// neighbourhood as B1's ~1.4k-token turns, and prompt_eval_count is reported so
// that claim is checkable rather than asserted.
const systemPrompt = `You are a voice assistant. You are speaking out loud to one person, in a conversation.

Your replies are converted to speech, so:
- Answer in one or two spoken sentences. Never use bullet points, numbered lists, headings, or markdown of any kind.
- Never read out code, file paths, or URLs unless you are explicitly asked for one.
- Do not restate the question before answering it.
- Do not narrate what you are about to do. Just do it, then say what happened.
- If you do not know something, say so in one sentence rather than guessing at length.

You are helping with software work on the user's machine. You have access to
tools for reading files, searching the codebase, and looking things up. Prefer
answering from what you already know; reach for a tool only when the answer
genuinely depends on something you cannot know without one.

The user is talking to you while doing something else. Brevity is not a style
preference here, it is the whole job: a reply that takes twenty seconds to speak
is a reply they will interrupt. Say the useful part first.`

// probeTools is a stand-in tool block sized to the production one. B1's
// diagnostic run put the real schemas at ~820 prompt tokens; this lands in the
// same range so the "tools on" condition is comparable.
func probeTools() api.Tools {
	obj := func(props map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": required}
	}
	str := func(desc string) map[string]any {
		return map[string]any{"type": "string", "description": desc}
	}
	specs := []struct {
		name, desc string
		params     map[string]any
	}{
		{"read_file", "Read a file from the user's machine and return its contents. Use when the answer depends on what is actually in a file rather than on general knowledge.",
			obj(map[string]any{"path": str("Absolute path to the file to read."), "start_line": map[string]any{"type": "integer", "description": "Optional first line to read, 1-based."}, "end_line": map[string]any{"type": "integer", "description": "Optional last line to read, inclusive."}}, "path")},
		{"write_file", "Write contents to a file on the user's machine, creating it if it does not exist. Overwrites the whole file, so read it first if you intend to preserve anything.",
			obj(map[string]any{"path": str("Absolute path to the file to write."), "content": str("The full new contents of the file.")}, "path", "content")},
		{"search_code", "Search the user's codebase for a pattern and return matching lines with their file paths and line numbers.",
			obj(map[string]any{"pattern": str("A regular expression to search for."), "path": str("Optional directory to restrict the search to."), "glob": str("Optional filename glob such as *.go to restrict which files are searched.")}, "pattern")},
		{"run_command", "Run a shell command on the user's machine and return its combined output. Only use this when the user has asked for something that genuinely requires running a command.",
			obj(map[string]any{"command": str("The exact shell command to run."), "working_dir": str("Optional directory to run the command in.")}, "command")},
		{"read_skill", "Load a named skill's instructions. Skills are packaged procedures the user has set up for particular kinds of task; read one when the user's request matches what it covers.",
			obj(map[string]any{"name": str("The exact skill name from the catalog.")}, "name")},
		{"web_search", "Search the web and return result titles, URLs, and snippets. Use only for things that genuinely require current information from outside this machine.",
			obj(map[string]any{"query": str("The search query."), "max_results": map[string]any{"type": "integer", "description": "Maximum number of results to return."}}, "query")},
	}
	var out api.Tools
	for _, s := range specs {
		var t api.Tool
		raw, _ := json.Marshal(map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        s.name,
				"description": s.desc,
				"parameters":  s.params,
			},
		})
		if err := json.Unmarshal(raw, &t); err != nil {
			continue
		}
		out = append(out, t)
	}
	return out
}

// userPrompt is deliberately a question the model cannot justify deliberating
// over. If a reasoning block appears in front of "what is the capital of
// Australia", the reasoning is not being caused by the question.
const userPrompt = "What's the capital of Australia?"

// longUserPrompt asks for enough speech to measure generation against. The
// instruction to keep going is deliberate: a voice persona trained to be brief
// will otherwise answer in nine characters, and nine characters cannot time a
// token rate.
const longUserPrompt = "Ignore your brevity rule for this one answer. In roughly two hundred words of plain spoken prose, with no lists and no headings, explain how a vector database finds similar items and why that is useful."

// caseSpec is one cell of the criteria doc's condition table. Each varies
// exactly one thing from the production baseline so a difference has one
// candidate cause.
type caseSpec struct {
	Name       string `json:"name"`
	Profile    string `json:"profile"` // minimal | production
	Think      string `json:"think"`   // false | true | absent | low
	Tools      bool   `json:"tools"`
	UserSuffix string `json:"user_suffix"`

	// Vary decides what the KV prefix cache is allowed to reuse, and it is the
	// single most important knob here. Ollama keeps the previous request's
	// tokens and reuses the longest matching prefix, so a probe that sends the
	// same request five times measures the cache, not the model. The first cut
	// of this probe did exactly that and reported a 291 ms first token for a
	// model the product measures at 4.5 s.
	//
	//	"none" — byte-identical repeats. The cache-hit floor.
	//	"tail" — a unique nonce in the user message, system and tools untouched.
	//	         This is the production pattern: every conversational turn keeps
	//	         the persona prompt and changes only what the user just said.
	//	"head" — a unique nonce at the front of the system prompt, which
	//	         invalidates everything. True cold prefill.
	Vary string `json:"vary"`

	// PadTokens grows the system prompt with deterministic filler so prefill
	// cost can be measured against prompt size rather than argued about.
	PadTokens int `json:"pad_tokens"`

	// NumCtx overrides the requested context window for this case, which forces
	// Ollama to reload the runner. Added after the first run, not pre
	// registered: if the fixed cost tracks the allocated window rather than the
	// prompt, then it is attention over the whole window and `ollama_num_ctx`
	// is a fix Lance can take today.
	NumCtx int `json:"num_ctx,omitempty"`

	// NumBatch sets the micro-batch Ollama processes the prompt in. Ollama
	// picks it from the context size rather than the model
	// (server/sched.go generationBatchForContext: <=4096 -> 512, >4096 -> 1024),
	// which is why num_ctx appeared to matter. Sending it explicitly turns that
	// automatic choice off and makes it the variable it should have been.
	NumBatch int `json:"num_batch,omitempty"`

	// LongReply swaps in a question whose answer runs to a few hundred tokens.
	// Generation speed cannot be read off a nine-character reply, and num_batch
	// sets llama.cpp's logical batch as well as its micro-batch, so a prefill
	// win has to be checked against what it costs the rest of the turn.
	LongReply bool   `json:"long_reply,omitempty"`
	Why       string `json:"why"`
}

// allCases is the criteria doc's condition table plus the cache-behaviour cases
// the first run showed were missing. The three `cache-*` rows are the load
// bearing ones: they differ only in what the KV prefix cache may reuse, so the
// gap between them is the cost of *not* reusing it.
func allCases() []caseSpec {
	return []caseSpec{
		// What the product actually does on every conversational turn.
		{Name: "cache-tail-vary", Profile: "production", Think: "false", Tools: true, Vary: "tail",
			Why: "THE PRODUCTION PATTERN: persona and tools fixed, only the user's new sentence changes"},
		{Name: "cache-exact-repeat", Profile: "production", Think: "false", Tools: true, Vary: "none",
			Why: "byte-identical repeats — the prefix-cache-hit floor, and what a naive probe measures by accident"},
		{Name: "cache-head-vary", Profile: "production", Think: "false", Tools: true, Vary: "head",
			Why: "nonce at the front of the system prompt: nothing is reusable, so this is true cold prefill"},

		// Does anything in the config surface move the production pattern?
		{Name: "prod-think-true", Profile: "production", Think: "true", Tools: true, Vary: "tail",
			Why: "does the think flag change anything at all?"},
		{Name: "prod-think-absent", Profile: "production", Think: "absent", Tools: true, Vary: "tail",
			Why: "is an explicit false different from omitting the field?"},
		{Name: "prod-nothink-marker", Profile: "production", Think: "false", Tools: true, Vary: "tail", UserSuffix: " /no_think",
			Why: "the qwen3 /no_think convention as a one-line fix candidate"},
		{Name: "prod-notools", Profile: "production", Think: "false", Tools: false, Vary: "tail",
			Why: "M6: how much of the cost is the tool schemas"},
		{Name: "prod-nosystem", Profile: "minimal", Think: "false", Tools: false, Vary: "tail",
			Why: "no system prompt and no tools — how much prompt is left to be slow"},

		// Prefill against prompt size, cache fully busted each time. A rate,
		// not a single number, so "slow prefill" becomes checkable.
		{Name: "prefill-cold-0500", Profile: "production", Think: "false", Tools: false, Vary: "head", PadTokens: 500,
			Why: "cold prefill rate at ~500 tokens"},
		{Name: "prefill-cold-1500", Profile: "production", Think: "false", Tools: false, Vary: "head", PadTokens: 1500,
			Why: "cold prefill rate at ~1500 tokens — the size B1 ran at"},
		{Name: "prefill-cold-3000", Profile: "production", Think: "false", Tools: false, Vary: "head", PadTokens: 3000,
			Why: "cold prefill rate at ~3000 tokens"},
		{Name: "prefill-cold-6000", Profile: "production", Think: "false", Tools: false, Vary: "head", PadTokens: 6000,
			Why: "cold prefill rate at ~6000 tokens — B1's filled-history size"},

		// Post-hoc, added after the first run showed a size-independent cost:
		// does it track the allocated context window instead of the prompt?
		{Name: "ctx-1024", Profile: "production", Think: "false", Tools: true, Vary: "tail", NumCtx: 1024,
			Why: "does the fixed cost scale with the allocated window rather than the prompt?"},
		{Name: "ctx-2048", Profile: "production", Think: "false", Tools: true, Vary: "tail", NumCtx: 2048, Why: "window sweep"},
		{Name: "ctx-4096", Profile: "production", Think: "false", Tools: true, Vary: "tail", NumCtx: 4096, Why: "window sweep"},
		{Name: "ctx-8192", Profile: "production", Think: "false", Tools: true, Vary: "tail", NumCtx: 8192, Why: "window sweep — today's production setting"},
		{Name: "ctx-16384", Profile: "production", Think: "false", Tools: true, Vary: "tail", NumCtx: 16384, Why: "window sweep"},

		// The fix candidate. The window sweep's step at 4096 is really a step
		// in micro-batch size, and Qwen3-Next/3.5's delta-net prefill kernel is
		// known upstream to lose throughput as that grows. Held at the
		// production window of 8192 so a win here is a win Lance can keep.
		{Name: "batch-1024", Profile: "production", Think: "false", Tools: true, Vary: "tail", NumBatch: 1024,
			Why: "what Ollama picks by itself at num_ctx 8192 — the control"},
		{Name: "batch-512", Profile: "production", Think: "false", Tools: true, Vary: "tail", NumBatch: 512, Why: "batch sweep"},
		{Name: "batch-256", Profile: "production", Think: "false", Tools: true, Vary: "tail", NumBatch: 256, Why: "batch sweep"},
		{Name: "batch-128", Profile: "production", Think: "false", Tools: true, Vary: "tail", NumBatch: 128, Why: "batch sweep"},
		{Name: "batch-64", Profile: "production", Think: "false", Tools: true, Vary: "tail", NumBatch: 64, Why: "batch sweep"},
		{Name: "batch-32", Profile: "production", Think: "false", Tools: true, Vary: "tail", NumBatch: 32, Why: "batch sweep"},

		// A small batch could plausibly win on a 1.2k prompt and lose on a real
		// conversation, so the fix is re-measured at B1's filled-history size
		// before it is believed.
		{Name: "batch-1024-6k", Profile: "production", Think: "false", Tools: true, Vary: "tail", NumBatch: 1024, PadTokens: 4500,
			Why: "control at ~6k tokens — Ollama's own choice"},
		{Name: "batch-256-6k", Profile: "production", Think: "false", Tools: true, Vary: "tail", NumBatch: 256, PadTokens: 4500,
			Why: "does the fix still hold at ~6k tokens?"},
		{Name: "batch-64-6k", Profile: "production", Think: "false", Tools: true, Vary: "tail", NumBatch: 64, PadTokens: 4500,
			Why: "does the smallest batch still win at ~6k tokens, or does it invert?"},

		// What the prefill win costs. num_batch is passed to llama.cpp as both
		// -b and -ub, so it is not only a prompt-reading knob.
		{Name: "gen-batch-1024", Profile: "production", Think: "false", Tools: true, Vary: "tail", NumBatch: 1024, LongReply: true,
			Why: "generation speed at Ollama's own batch — the control"},
		{Name: "gen-batch-512", Profile: "production", Think: "false", Tools: true, Vary: "tail", NumBatch: 512, LongReply: true, Why: "generation speed sweep"},
		{Name: "gen-batch-256", Profile: "production", Think: "false", Tools: true, Vary: "tail", NumBatch: 256, LongReply: true, Why: "generation speed sweep"},
		{Name: "gen-batch-128", Profile: "production", Think: "false", Tools: true, Vary: "tail", NumBatch: 128, LongReply: true, Why: "generation speed sweep"},
		{Name: "gen-batch-64", Profile: "production", Think: "false", Tools: true, Vary: "tail", NumBatch: 64, LongReply: true, Why: "generation speed sweep"},
	}
}

// controlCases are what the fast models run. They exist to calibrate the
// instrument, not to be optimized: if the probe cannot reproduce gemma4's
// sub-400 ms TTFT on the production pattern, the probe is wrong and nothing it
// says about the incumbent can be trusted.
func controlCases() []caseSpec {
	all := allCases()
	keep := map[string]bool{
		"cache-tail-vary": true, "cache-exact-repeat": true, "cache-head-vary": true,
		"prefill-cold-0500": true, "prefill-cold-1500": true, "prefill-cold-3000": true, "prefill-cold-6000": true,
	}
	var out []caseSpec
	for _, c := range all {
		if keep[c.Name] {
			out = append(out, c)
		}
	}
	return out
}

// padWords is deterministic filler: the same text for every model and every
// run, so a size sweep compares like with like. It is prose rather than
// repeated tokens because a tokenizer collapses repetition and the point is to
// buy a known number of tokens.
var padWords = strings.Fields(`the quick brown fox jumps over a lazy dog while seventeen careful engineers
measure latency in a warm room and write down what they saw before arguing about
what it meant because a number without a method is only an opinion wearing a
uniform and the machine does not care which of them is louder today`)

func padding(tokens int) string {
	if tokens <= 0 {
		return ""
	}
	// ~0.75 tokens per word for ordinary English prose; the artifact reports
	// the real prompt_eval_count, so this only has to be close.
	want := (tokens * 4) / 3
	var b strings.Builder
	b.WriteString("\n\nReference notes, for context only:\n")
	for i := 0; i < want; i++ {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(padWords[i%len(padWords)])
		if i%17 == 16 {
			b.WriteByte('.')
		}
	}
	return b.String()
}

type sample struct {
	Case  string `json:"case"`
	Model string `json:"model"`
	Rep   int    `json:"rep"`

	// Wall clock from request send. FirstContentMS is TTFT as B1 defined it and
	// as the user experiences it: the first byte of something speakable.
	FirstAnyMS      float64 `json:"first_any_ms"`
	FirstThinkingMS float64 `json:"first_thinking_ms,omitempty"`
	FirstContentMS  float64 `json:"first_content_ms"`
	TotalMS         float64 `json:"total_ms"`

	ThinkingChars int     `json:"thinking_chars"`
	ContentChars  int     `json:"content_chars"`
	HiddenShare   float64 `json:"hidden_share"`
	Thinking      string  `json:"thinking,omitempty"`
	Content       string  `json:"content"`

	// ContentHasThinkTag is the qwen3:30b failure mode: reasoning arriving as
	// ordinary content, which the pipeline would speak aloud.
	ContentHasThinkTag bool     `json:"content_has_think_tag"`
	ToolCalls          []string `json:"tool_calls,omitempty"`

	// Ollama's own accounting. B1 captured only the two counts; the durations
	// are what separate prefill from generation.
	LoadDurationMS       float64 `json:"load_duration_ms"`
	PromptEvalCount      int     `json:"prompt_eval_count"`
	PromptEvalDurationMS float64 `json:"prompt_eval_duration_ms"`
	EvalCount            int     `json:"eval_count"`
	EvalDurationMS       float64 `json:"eval_duration_ms"`
	TotalDurationMS      float64 `json:"total_duration_ms"`

	PrefillTokPerS float64 `json:"prefill_tok_per_s"`
	GenTokPerS     float64 `json:"gen_tok_per_s"`

	// UnaccountedMS is wall clock minus what Ollama says it spent. M7 lives
	// here: a fixed cost that belongs to none of the reported phases.
	UnaccountedMS float64 `json:"unaccounted_ms"`

	Err string `json:"error,omitempty"`
}

type caseSummary struct {
	Case string   `json:"case"`
	Why  string   `json:"why"`
	N    int      `json:"n"`
	Errs int      `json:"errors"`
	Spec caseSpec `json:"spec"`

	FirstContentP50MS  float64 `json:"first_content_p50_ms"`
	FirstContentP95MS  float64 `json:"first_content_p95_ms"`
	FirstAnyP50MS      float64 `json:"first_any_p50_ms"`
	FirstThinkingP50MS float64 `json:"first_thinking_p50_ms"`
	TotalP50MS         float64 `json:"total_p50_ms"`

	ThinkingCharsMedian           int     `json:"thinking_chars_median"`
	ContentCharsMedian            int     `json:"content_chars_median"`
	HiddenShareMedian             float64 `json:"hidden_share_median"`
	AnySampleHasThinkTagInContent bool    `json:"any_sample_has_think_tag_in_content"`

	PromptEvalCountMedian   int     `json:"prompt_eval_count_median"`
	PromptEvalDurationP50MS float64 `json:"prompt_eval_duration_p50_ms"`
	EvalCountMedian         int     `json:"eval_count_median"`
	EvalDurationP50MS       float64 `json:"eval_duration_p50_ms"`
	LoadDurationP50MS       float64 `json:"load_duration_p50_ms"`
	PrefillTokPerSMedian    float64 `json:"prefill_tok_per_s_median"`
	GenTokPerSMedian        float64 `json:"gen_tok_per_s_median"`
	UnaccountedP50MS        float64 `json:"unaccounted_p50_ms"`

	// GateTTFT is the criteria doc's bar: p50 first_content <= 1500 ms.
	GateTTFT     bool `json:"gate_ttft_p50_le_1500ms"`
	GateClean    bool `json:"gate_no_think_tag_in_content"`
	GateAnswered bool `json:"gate_every_sample_answered"`
	GatePass     bool `json:"gate_pass"`
}

type modelResult struct {
	Model     string `json:"model"`
	Digest    string `json:"digest,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
	Renderer  string `json:"renderer,omitempty"`
	Parser    string `json:"parser,omitempty"`
	Arch      string `json:"architecture,omitempty"`
	Requires  string `json:"requires_ollama,omitempty"`
	// HasProjector records M5: the incumbent ships a 460M-param CLIP projector
	// that gemma4 does not report at all.
	HasProjector bool          `json:"has_vision_projector"`
	Capabilities []string      `json:"capabilities,omitempty"`
	Cases        []caseSummary `json:"cases"`
	Samples      []sample      `json:"samples"`
	SetupErr     string        `json:"setup_error,omitempty"`
}

type artifact struct {
	ID           string           `json:"id"`
	WorkItem     string           `json:"work_item"`
	Title        string           `json:"title"`
	CriteriaDoc  string           `json:"criteria_doc"`
	GeneratedAt  string           `json:"generated_at"`
	Harness      string           `json:"harness"`
	Command      string           `json:"command"`
	Provenance   map[string]any   `json:"provenance"`
	RequestShape map[string]any   `json:"request_shape"`
	SystemPrompt string           `json:"system_prompt"`
	UserPrompt   string           `json:"user_prompt"`
	Cases        []caseSpec       `json:"cases"`
	Results      []modelResult    `json:"results"`
	RunningAfter []map[string]any `json:"running_models_after"`
	Notes        []string         `json:"notes"`
}

func main() {
	var (
		modelsFlag  = flag.String("models", "qwen3.8-uncensored:27b", "comma-separated models to probe with the full case matrix")
		controlFlag = flag.String("controls", "gemma4:26b,qwen3:8b", "comma-separated control models (calibration cases only)")
		reps        = flag.Int("reps", 5, "measured samples per case")
		rewarm      = flag.String("rewarm", "qwen3.8-uncensored:27b", "model to leave resident at the end, warmed the way the product warms it")
		out         = flag.String("out", "", "artifact path (default docs/benchmarks/B2-qwen-ttft-mechanism-<date>.json)")
		casesFlag   = flag.String("cases", "", "comma-separated case names to run (default: all)")
		timeout     = flag.Duration("timeout", 5*time.Minute, "per-sample timeout")

		proxyListen   = flag.String("proxy", "", "run as a recording proxy on this address instead of probing (e.g. 127.0.0.1:11500)")
		proxyUpstream = flag.String("proxy-upstream", "http://127.0.0.1:11434", "real ollama address the proxy forwards to")
		proxyOut      = flag.String("proxy-out", "ttft-proxy-capture.jsonl", "where the proxy writes captured requests")
	)
	flag.Parse()

	if *proxyListen != "" {
		if err := runProxy(*proxyListen, *proxyUpstream, *proxyOut); err != nil {
			fatal("proxy: %v", err)
		}
		return
	}

	ctx := context.Background()
	client, err := api.ClientFromEnvironment()
	if err != nil {
		fatal("ollama client: %v", err)
	}
	if err := client.Heartbeat(ctx); err != nil {
		fatal("ollama is not reachable: %v", err)
	}

	art := &artifact{
		ID:          "B2",
		WorkItem:    "WI-adf58f",
		Title:       "qwen3.8 fixed TTFT: mechanism",
		CriteriaDoc: "docs/benchmarks/B2-qwen-ttft-mechanism-criteria.md",
		GeneratedAt: time.Now().Format(time.RFC3339),
		Harness:     "scripts/ttft_probe",
		Command:     strings.Join(os.Args, " "),
		Provenance:  provenance(ctx, client),
		RequestShape: map[string]any{
			"endpoint":   "/api/chat (ollama api.Client.Chat)",
			"stream":     true,
			"num_ctx":    probeNumCtx,
			"keep_alive": probeKeepAlive.Duration.String(),
			"note":       "num_ctx is always sent: omitting it loads the qwen3.8 tags at their advertised 262144 window and pins RAM",
		},
		SystemPrompt: systemPrompt,
		UserPrompt:   userPrompt,
	}

	wanted := map[string]bool{}
	for _, c := range splitCSV(*casesFlag) {
		wanted[c] = true
	}
	filter := func(in []caseSpec) []caseSpec {
		if len(wanted) == 0 {
			return in
		}
		var keep []caseSpec
		for _, c := range in {
			if wanted[c.Name] {
				keep = append(keep, c)
			}
		}
		return keep
	}

	art.Cases = filter(allCases())

	type job struct {
		model string
		cases []caseSpec
	}
	var jobs []job
	for _, m := range splitCSV(*modelsFlag) {
		jobs = append(jobs, job{m, filter(allCases())})
	}
	for _, m := range splitCSV(*controlFlag) {
		jobs = append(jobs, job{m, filter(controlCases())})
	}

	for _, j := range jobs {
		if len(j.cases) == 0 {
			continue
		}
		fmt.Fprintf(os.Stderr, "\n=== %s ===\n", j.model)
		// Serialized: nothing else stays resident while this model is measured.
		unloadAllExcept(ctx, client, j.model)
		res := probeModel(ctx, client, j.model, j.cases, *reps, *timeout)
		art.Results = append(art.Results, res)
	}

	// Leave the machine as it was found: the incumbent resident, at the window
	// the product uses, warmed with the product's own Warmup shape.
	if *rewarm != "" {
		fmt.Fprintf(os.Stderr, "\nre-warming %s at num_ctx=%d\n", *rewarm, probeNumCtx)
		unloadAllExcept(ctx, client, *rewarm)
		if err := warmup(ctx, client, *rewarm); err != nil {
			fmt.Fprintf(os.Stderr, "rewarm failed: %v\n", err)
			art.Notes = append(art.Notes, fmt.Sprintf("rewarm of %s failed: %v", *rewarm, err))
		}
	}
	art.RunningAfter = runningModels(ctx, client)

	path := *out
	if path == "" {
		path = fmt.Sprintf("docs/benchmarks/B2-qwen-ttft-mechanism-%s.json", time.Now().Format("2006-01-02"))
	}
	blob, err := json.MarshalIndent(art, "", "  ")
	if err != nil {
		fatal("marshal: %v", err)
	}
	if err := os.WriteFile(path, append(blob, '\n'), 0o644); err != nil {
		fatal("write %s: %v", path, err)
	}
	fmt.Fprintf(os.Stderr, "\nwrote %s\n", path)
	printTable(art)
}

func probeModel(ctx context.Context, client *api.Client, model string, cases []caseSpec, reps int, timeout time.Duration) modelResult {
	res := modelResult{Model: model}
	if info, err := client.Show(ctx, &api.ShowRequest{Model: model}); err == nil {
		for _, c := range info.Capabilities {
			res.Capabilities = append(res.Capabilities, fmt.Sprint(c))
		}
		res.Arch = info.Details.Family
		// Renderer/Parser are the structural difference this probe is chasing.
		// A model naming one here does not use the Go TEMPLATE the fast qwen3
		// tags use — its think handling is compiled into Ollama, where nobody
		// configuring the product can read it.
		res.Renderer, res.Parser = info.Renderer, info.Parser
		res.HasProjector = len(info.ProjectorInfo) > 0
		res.Requires = info.Requires
	}
	if list, err := client.List(ctx); err == nil {
		for _, m := range list.Models {
			if m.Name == model {
				res.Digest = m.Digest
				res.SizeBytes = m.Size
			}
		}
	}

	// Load the model the way the product loads it, then throw one sample away.
	// Ollama keeps the previous request's KV state, so the first measured
	// sample of a cold runner is measuring something else.
	if err := warmup(ctx, client, model); err != nil {
		res.SetupErr = err.Error()
		return res
	}

	for _, c := range cases {
		fmt.Fprintf(os.Stderr, "  %-26s ", c.Name)
		// Discard sample: settles the runner for this case's prompt shape. For
		// the "none" case it also primes the cache the case exists to measure.
		_ = runOnce(ctx, client, model, c, nonceFor(c, 0), timeout)
		var got []sample
		for r := 1; r <= reps; r++ {
			s := runOnce(ctx, client, model, c, nonceFor(c, r), timeout)
			s.Case, s.Model, s.Rep = c.Name, model, r
			got = append(got, s)
			if s.Err != "" {
				fmt.Fprintf(os.Stderr, "E")
			} else {
				fmt.Fprintf(os.Stderr, ".")
			}
		}
		sum := summarize(c, got)
		res.Cases = append(res.Cases, sum)
		res.Samples = append(res.Samples, got...)
		fmt.Fprintf(os.Stderr, "  content p50 %.0fms  any p50 %.0fms  think %d ch  prefill %.0fms\n",
			sum.FirstContentP50MS, sum.FirstAnyP50MS, sum.ThinkingCharsMedian, sum.PromptEvalDurationP50MS)
	}
	return res
}

func runOnce(ctx context.Context, client *api.Client, model string, c caseSpec, nonce string, timeout time.Duration) sample {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var msgs []api.Message
	var tools api.Tools
	if c.Profile == "production" {
		sys := systemPrompt + padding(c.PadTokens)
		if c.Vary == "head" {
			// In front of everything, so not one cached token survives.
			sys = "Session reference " + nonce + ".\n\n" + sys
		}
		msgs = append(msgs, api.Message{Role: "system", Content: sys})
		if c.Tools {
			tools = probeTools()
		}
	}
	base := userPrompt
	if c.LongReply {
		base = longUserPrompt
	}
	user := base + c.UserSuffix
	if c.Vary == "tail" {
		// A new sentence at the end of the user turn — exactly what a real
		// conversational turn changes, and nothing more.
		user = user + " (note " + nonce + ")"
	}
	msgs = append(msgs, api.Message{Role: "user", Content: user})

	numCtx := probeNumCtx
	if c.NumCtx > 0 {
		numCtx = c.NumCtx
	}
	opts := map[string]any{"num_ctx": numCtx}
	if c.NumBatch > 0 {
		opts["num_batch"] = c.NumBatch
	}
	stream := true
	req := &api.ChatRequest{
		Model:     model,
		Messages:  msgs,
		Tools:     tools,
		Stream:    &stream,
		KeepAlive: probeKeepAlive,
		Options:   opts,
	}
	// "absent" is the point of the condition: a nil Think omits the field
	// entirely, which is a different request from an explicit false.
	if c.Think != "absent" {
		req.Think = thinkValue(c.Think)
	}

	var s sample
	var content, thinking strings.Builder
	start := time.Now()
	var firstAny, firstThinking, firstContent time.Time

	err := client.Chat(cctx, req, func(r api.ChatResponse) error {
		now := time.Now()
		if r.Message.Thinking != "" || r.Message.Content != "" || len(r.Message.ToolCalls) > 0 {
			if firstAny.IsZero() {
				firstAny = now
			}
		}
		if r.Message.Thinking != "" {
			if firstThinking.IsZero() {
				firstThinking = now
			}
			thinking.WriteString(r.Message.Thinking)
		}
		if r.Message.Content != "" {
			if firstContent.IsZero() {
				firstContent = now
			}
			content.WriteString(r.Message.Content)
		}
		for _, tc := range r.Message.ToolCalls {
			s.ToolCalls = append(s.ToolCalls, tc.Function.Name)
		}
		if r.Done {
			s.LoadDurationMS = ms(r.Metrics.LoadDuration)
			s.PromptEvalCount = r.Metrics.PromptEvalCount
			s.PromptEvalDurationMS = ms(r.Metrics.PromptEvalDuration)
			s.EvalCount = r.Metrics.EvalCount
			s.EvalDurationMS = ms(r.Metrics.EvalDuration)
			s.TotalDurationMS = ms(r.Metrics.TotalDuration)
		}
		return nil
	})
	s.TotalMS = msSince(start)
	if err != nil {
		s.Err = err.Error()
	}

	s.Content = content.String()
	s.Thinking = thinking.String()
	s.ContentChars = len(s.Content)
	s.ThinkingChars = len(s.Thinking)
	if tot := s.ContentChars + s.ThinkingChars; tot > 0 {
		s.HiddenShare = float64(s.ThinkingChars) / float64(tot)
	}
	s.ContentHasThinkTag = strings.Contains(s.Content, "<think>") || strings.Contains(s.Content, "</think>")

	if !firstAny.IsZero() {
		s.FirstAnyMS = float64(firstAny.Sub(start).Microseconds()) / 1000
	}
	if !firstThinking.IsZero() {
		s.FirstThinkingMS = float64(firstThinking.Sub(start).Microseconds()) / 1000
	}
	if !firstContent.IsZero() {
		s.FirstContentMS = float64(firstContent.Sub(start).Microseconds()) / 1000
	} else {
		// No speakable byte ever arrived. Charging the whole turn is the honest
		// reading: that is exactly what the user would have waited through.
		s.FirstContentMS = s.TotalMS
	}
	if s.PromptEvalDurationMS > 0 {
		s.PrefillTokPerS = float64(s.PromptEvalCount) / (s.PromptEvalDurationMS / 1000)
	}
	if s.EvalDurationMS > 0 {
		s.GenTokPerS = float64(s.EvalCount) / (s.EvalDurationMS / 1000)
	}
	s.UnaccountedMS = s.TotalMS - (s.LoadDurationMS + s.PromptEvalDurationMS + s.EvalDurationMS)
	return s
}

func summarize(c caseSpec, ss []sample) caseSummary {
	sum := caseSummary{Case: c.Name, Why: c.Why, Spec: c, N: len(ss)}
	var fc, fa, ft, tot, ped, ed, ld, pref, gen, unacc []float64
	var thch, coch, pec, ec []float64
	answered := true
	for _, s := range ss {
		if s.Err != "" {
			sum.Errs++
			continue
		}
		fc = append(fc, s.FirstContentMS)
		fa = append(fa, s.FirstAnyMS)
		if s.FirstThinkingMS > 0 {
			ft = append(ft, s.FirstThinkingMS)
		}
		tot = append(tot, s.TotalMS)
		ped = append(ped, s.PromptEvalDurationMS)
		ed = append(ed, s.EvalDurationMS)
		ld = append(ld, s.LoadDurationMS)
		pref = append(pref, s.PrefillTokPerS)
		gen = append(gen, s.GenTokPerS)
		unacc = append(unacc, s.UnaccountedMS)
		thch = append(thch, float64(s.ThinkingChars))
		coch = append(coch, float64(s.ContentChars))
		pec = append(pec, float64(s.PromptEvalCount))
		ec = append(ec, float64(s.EvalCount))
		if strings.TrimSpace(s.Content) == "" {
			answered = false
		}
		if s.ContentHasThinkTag {
			sum.AnySampleHasThinkTagInContent = true
		}
	}
	sum.FirstContentP50MS = pct(fc, 50)
	sum.FirstContentP95MS = pct(fc, 95)
	sum.FirstAnyP50MS = pct(fa, 50)
	sum.FirstThinkingP50MS = pct(ft, 50)
	sum.TotalP50MS = pct(tot, 50)
	sum.PromptEvalDurationP50MS = pct(ped, 50)
	sum.EvalDurationP50MS = pct(ed, 50)
	sum.LoadDurationP50MS = pct(ld, 50)
	sum.PrefillTokPerSMedian = pct(pref, 50)
	sum.GenTokPerSMedian = pct(gen, 50)
	sum.UnaccountedP50MS = pct(unacc, 50)
	sum.ThinkingCharsMedian = int(pct(thch, 50))
	sum.ContentCharsMedian = int(pct(coch, 50))
	sum.PromptEvalCountMedian = int(pct(pec, 50))
	sum.EvalCountMedian = int(pct(ec, 50))
	if m := sum.ThinkingCharsMedian + sum.ContentCharsMedian; m > 0 {
		sum.HiddenShareMedian = float64(sum.ThinkingCharsMedian) / float64(m)
	}
	sum.GateTTFT = len(fc) > 0 && sum.FirstContentP50MS <= 1500
	sum.GateClean = !sum.AnySampleHasThinkTagInContent
	sum.GateAnswered = answered && sum.Errs == 0 && len(fc) > 0
	sum.GatePass = sum.GateTTFT && sum.GateClean && sum.GateAnswered
	return sum
}

// nonceFor makes each sample's prompt unique except where the case is
// deliberately measuring a repeat. Deterministic per (case, rep) so a re-run
// sends the same bytes as the run before it.
func nonceFor(c caseSpec, rep int) string {
	if c.Vary == "" || c.Vary == "none" {
		return "fixed"
	}
	return fmt.Sprintf("%s-%03d", c.Name, rep)
}

// warmup reproduces brain.Warmup exactly (ollama.go:553-576). Using a different
// shape here is the documented RAM trap: a request without num_ctx loads the
// qwen3.8 tags at 262144.
func warmup(ctx context.Context, client *api.Client, model string) error {
	stream := false
	req := &api.ChatRequest{
		Model:     model,
		Messages:  []api.Message{{Role: "user", Content: "hi"}},
		Stream:    &stream,
		Think:     thinkValue("false"),
		KeepAlive: probeKeepAlive,
		Options: map[string]any{
			"num_predict": 1,
			"num_ctx":     probeNumCtx,
		},
	}
	wctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	return client.Chat(wctx, req, func(api.ChatResponse) error { return nil })
}

func thinkValue(s string) *api.ThinkValue {
	switch s {
	case "true":
		return &api.ThinkValue{Value: true}
	case "false":
		return &api.ThinkValue{Value: false}
	default:
		return &api.ThinkValue{Value: s}
	}
}

// unloadAllExcept expires every resident model but keep. Unloading is a
// keep_alive of zero — it never deletes anything from disk.
func unloadAllExcept(ctx context.Context, client *api.Client, keep string) {
	running, err := client.ListRunning(ctx)
	if err != nil {
		return
	}
	zero := &api.Duration{}
	for _, m := range running.Models {
		if m.Name == keep {
			continue
		}
		fmt.Fprintf(os.Stderr, "  unloading %s\n", m.Name)
		_ = client.Generate(ctx, &api.GenerateRequest{Model: m.Name, KeepAlive: zero}, func(api.GenerateResponse) error { return nil })
	}
}

func runningModels(ctx context.Context, client *api.Client) []map[string]any {
	running, err := client.ListRunning(ctx)
	if err != nil {
		return nil
	}
	var out []map[string]any
	for _, m := range running.Models {
		out = append(out, map[string]any{
			"name":       m.Name,
			"context":    m.ContextLength,
			"size_bytes": m.Size,
			"expires_at": m.ExpiresAt.Format(time.RFC3339),
		})
	}
	return out
}

func provenance(ctx context.Context, client *api.Client) map[string]any {
	p := map[string]any{
		"commit":         strings.TrimSpace(sh("git", "rev-parse", "HEAD")),
		"branch":         strings.TrimSpace(sh("git", "rev-parse", "--abbrev-ref", "HEAD")),
		"worktree_clean": strings.TrimSpace(sh("git", "status", "--porcelain")) == "",
		"machine":        strings.TrimSpace(sh("sysctl", "-n", "hw.model")),
		"cpu":            strings.TrimSpace(sh("sysctl", "-n", "machdep.cpu.brand_string")),
		"os":             strings.TrimSpace(sh("sw_vers", "-productName")) + " " + strings.TrimSpace(sh("sw_vers", "-productVersion")),
		"goarch":         runtime.GOARCH,
	}
	if n, err := strconv.ParseInt(strings.TrimSpace(sh("sysctl", "-n", "hw.memsize")), 10, 64); err == nil {
		p["memory_bytes"] = n
	}
	if v, err := client.Version(ctx); err == nil {
		p["ollama_version"] = v
	}
	return p
}

func printTable(a *artifact) {
	fmt.Printf("\n%-26s %-20s %8s %9s %9s %7s %9s %7s %6s\n",
		"model", "case", "promptTk", "content", "prefill", "pf tok/s", "eval", "thinkCh", "gate")
	for _, r := range a.Results {
		for _, c := range r.Cases {
			gate := "FAIL"
			if c.GatePass {
				gate = "pass"
			}
			fmt.Printf("%-26s %-20s %8d %8.0fms %8.0fms %7.0f %8.0fms %7d %6s\n",
				r.Model, c.Case, c.PromptEvalCountMedian, c.FirstContentP50MS,
				c.PromptEvalDurationP50MS, c.PrefillTokPerSMedian,
				c.EvalDurationP50MS, c.ThinkingCharsMedian, gate)
		}
	}
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func pct(v []float64, p int) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	if p >= 100 {
		return s[len(s)-1]
	}
	i := (p * len(s)) / 100
	if i >= len(s) {
		i = len(s) - 1
	}
	return s[i]
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }

func msSince(t time.Time) float64 { return float64(time.Since(t).Microseconds()) / 1000 }

func sh(name string, args ...string) string {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return ""
	}
	return string(out)
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "ttft_probe: "+format+"\n", args...)
	os.Exit(1)
}
