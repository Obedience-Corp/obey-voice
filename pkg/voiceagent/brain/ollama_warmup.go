package brain

import (
	"context"
	"fmt"
	"os"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/delegation"
	"github.com/ollama/ollama/api"
)

// defaultWarmupNumCtx is used when ollama_num_ctx is unset (0). Omitting
// num_ctx lets Ollama size the runner to the model's advertised window —
// 262144 on qwen3.8 / kimi — and a supervisor restart loop pins those
// runners until keep_alive expires. That is not a leak; it is a reload.
const defaultWarmupNumCtx = 8192

func (o *OllamaBrain) warmupNumCtx(highWater int) int {
	if o.cfg != nil && o.cfg.OllamaNumCtx > 0 {
		return o.numCtxFor(highWater)
	}
	return defaultWarmupNumCtx
}

// Warmup preloads the model and primes the server's prompt cache with the
// prefix the first real turn will send: the frozen system prompt and the same
// tool schema the live path uses. Loading the model alone was not enough —
// with a bare "hi" the cache held a 13-token prompt, so the first sentence
// still paid a full read of the persona (about 5 s on qwen3.8). With the real
// prefix in the cache the first turn restores a checkpoint and reads only the
// user's words. Best-effort: it caps generation at one token and ignores all
// errors so it can never block or disrupt startup.
//
// The request goes through the same chat wrapper as a live turn, so a model
// that rejects tools (kimi) or think is retried without them and still gets
// loaded; a bare client call would 400 before the load and leave the first
// turn colder than the old "hi" did. Warmup runs on its own goroutine while
// startup may still be restoring a session and the first turn may already be
// running, so the prompt, delegation runtime and context high-water mark it
// reads are snapshotted under stateMu, which their writers also take.
//
// Always send an explicit num_ctx. A request with no options inherits the
// model default (262k on current qwen/kimi tags) and is what filled RAM
// when serve was being respawned.
func (o *OllamaBrain) Warmup(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	stream := false
	// Build on the same options a real turn sends, rather than assembling a
	// second map by hand. Ollama reloads the model whenever a request's options
	// differ from the loaded runner's, so any drift between these two sets buys
	// a model load that the user's first sentence pays for.
	o.stateMu.Lock()
	prompt, delegation, highWater := o.fullSystemPrompt, o.delegation, o.contextHighWater
	o.stateMu.Unlock()
	opts := o.requestOptionsFor(highWater)
	if opts == nil {
		opts = map[string]any{}
	}
	opts["num_predict"] = 1
	// The one deliberate difference: with ollama_num_ctx unset a chat request
	// defers to the server, but loading the model that way sizes the runner to
	// the tag's advertised window (262144 on qwen3.8) and pins the RAM. Warmup
	// floors it instead. The two then disagree and the first real turn reloads
	// — pinned by TestWarmupMatchesChatOptions/window_unset_diverges, which is
	// where to start if that reload is ever worth closing.
	opts["num_ctx"] = o.warmupNumCtx(highWater)
	messages := warmupMessages(prompt + historicalIdentityGuidance)
	tools := o.warmupTools(delegation)
	fmt.Fprintf(os.Stderr, "obey-voice: warmup model=%s num_ctx=%v num_batch=%v prefix_chars=%d tools=%d\n", o.model, opts["num_ctx"], opts["num_batch"], len(messages[0].Content), len(tools))
	req := &api.ChatRequest{
		Model:     o.model,
		Messages:  messages,
		Tools:     tools,
		Stream:    &stream,
		Think:     o.resolvedThink(),
		KeepAlive: o.keepAlive,
		Options:   opts,
	}
	_ = o.chat(ctx, req, func(api.ChatResponse) error { return nil })
}

// warmupMessages is the first turn's prefix followed by a throwaway user line.
// The system message must be byte-identical to what buildMessages sends, or
// the cache primed here is never hit. The user line is required: Ollama
// rejects a chat with no user message (ollama#17778), and it sits after the
// checkpoint the first real turn restores from, so its content is irrelevant.
func warmupMessages(prompt string) []api.Message {
	if prompt == "" {
		return []api.Message{{Role: "user", Content: "hi"}}
	}
	return []api.Message{
		{Role: "system", Content: prompt},
		{Role: "user", Content: "hi"},
	}
}

// warmupTools mirrors the live path's tool selection (pipeline
// liveRouterToolsPolicy): Ollama renders the tool schema ahead of the system
// prompt, so a warmup without it primes a prefix the first turn never matches.
func (o *OllamaBrain) warmupTools(delegation *delegation.Runtime) api.Tools {
	if o.cfg == nil || !o.cfg.VoiceToolsEnabled {
		return nil
	}
	sess := &toolSession{
		catalog:      o.skills,
		routerPolicy: NormalizeRouterToolsPolicy(o.cfg.VoiceRouterTools),
	}
	if delegation != nil && delegation.Enabled() {
		sess.delegation = delegation
	}
	return sess.tools()
}
