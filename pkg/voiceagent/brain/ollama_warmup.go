package brain

import (
	"context"
	"fmt"
	"os"

	"github.com/ollama/ollama/api"
)

// defaultWarmupNumCtx is used when ollama_num_ctx is unset (0). Omitting
// num_ctx lets Ollama size the runner to the model's advertised window —
// 262144 on qwen3.8 / kimi — and a supervisor restart loop pins those
// runners until keep_alive expires. That is not a leak; it is a reload.
const defaultWarmupNumCtx = 8192

func (o *OllamaBrain) warmupNumCtx() int {
	if o.cfg != nil && o.cfg.OllamaNumCtx > 0 {
		return o.effectiveNumCtx()
	}
	return defaultWarmupNumCtx
}

// Warmup preloads the model into memory with a minimal request so the user's
// first real turn doesn't pay the cold-start (model-load) cost. Best-effort:
// it caps generation, sends no tools, and ignores all errors so it can never
// block or disrupt startup.
//
// Always send an explicit num_ctx. A "hi" with no options inherits the
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
	opts := o.requestOptions()
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
	opts["num_ctx"] = o.warmupNumCtx()
	fmt.Fprintf(os.Stderr, "obey-voice: warmup model=%s num_ctx=%v num_batch=%v\n", o.model, opts["num_ctx"], opts["num_batch"])
	req := &api.ChatRequest{
		Model:     o.model,
		Messages:  []api.Message{{Role: "user", Content: "hi"}},
		Stream:    &stream,
		Think:     o.resolvedThink(),
		KeepAlive: o.keepAlive,
		Options:   opts,
	}
	_ = o.client.Chat(ctx, req, func(api.ChatResponse) error { return nil })
}
