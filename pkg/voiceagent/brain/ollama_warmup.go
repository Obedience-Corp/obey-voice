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

func (o *OllamaBrain) warmupNumCtx(highWater int) int {
	if o.audioResolver != nil || (o.cfg != nil && o.cfg.OllamaNumCtx > 0) {
		return o.numCtxFor(highWater)
	}
	return defaultWarmupNumCtx
}

// Warmup is WarmupTurn with the routing this brain's configuration gives a
// live turn, for a host that has no pipeline to ask.
func (o *OllamaBrain) Warmup(ctx context.Context) {
	o.WarmupTurn(ctx, o.configuredTurnOptions())
}

func (o *OllamaBrain) configuredTurnOptions() StreamOptions {
	if o.cfg == nil {
		return StreamOptions{}
	}
	tiers := workerTierAvailability(o.cfg)
	opts := StreamOptions{
		WorkerTiers:       tiers,
		ToolsEnabled:      o.cfg.VoiceToolsEnabled || tiers != nil,
		RouterToolsPolicy: NormalizeRouterToolsPolicy(o.cfg.VoiceRouterTools),
		RouteJSON:         JSONRouteEnabled(o.cfg.VoiceRouterTools),
	}
	if tiers != nil {
		opts.RouterToolsPolicy = RouterToolsRouteOnly
	}
	return opts
}

// WarmupTurn preloads the model and primes the server's prompt cache with the
// request the first turn sends ahead of the user's words. opts are the routing
// options the host gives an ordinary turn. The system message, tools and
// options are built by the same code as a live turn's, so the first turn
// restores a checkpoint and reads only what the user said. Loading the model
// alone was not enough: with a bare "hi" the cache held a 13-token prompt and
// the first sentence still paid a full read of the persona.
//
// Best-effort: generation is capped at one token and errors are ignored. The
// request goes through the same chat wrapper as a live turn, so unsupported
// routing fails compatibility while legacy tools and think keep their fallback.
// It sends nothing once a turn has reached this brain: a warmup queued behind
// the turn's first request would delay the turn's next one on a runner that
// serves one request at a time. A request already sent may finish.
//
// WarmupTurn runs on its own goroutine while startup may still be restoring a
// session and the first turn may already be running. It reads only what is
// fixed at construction or guarded by stateMu, and the request it sends holds
// no conversation history.
//
// Always send an explicit num_ctx. A request with no options inherits the
// model default (262k on current qwen/kimi tags) and is what filled RAM
// when serve was being respawned.
func (o *OllamaBrain) WarmupTurn(ctx context.Context, opts StreamOptions) {
	if ctx.Err() != nil || o.turnStarted.Load() {
		return
	}
	o.stateMu.Lock()
	prompt, highWater := o.fullSystemPrompt, o.contextHighWater
	o.stateMu.Unlock()
	var sess *toolSession
	var tools api.Tools
	if opts.ToolsEnabled {
		sess = o.turnToolSession(opts)
		tools = sess.tools()
	}
	// Ollama rejects a chat with no user message (ollama#17778). The line sits
	// after the prefix the first turn restores, so its words do not matter.
	user := api.Message{Role: "user", Content: encodeOllamaUser(opts.Speaker, "hi")}
	messages := o.buildMessagesFrom(turnView{prompt: prompt, history: []api.Message{user}}, "", false, opts)
	// json_route asks for the route on the reply's own messages, without tools.
	if sess != nil && sess.routerPolicy == RouterToolsRouteOnly && opts.RouteJSON && jsonRouteEligible(tools, messages) {
		tools = nil
	}
	options := o.requestOptionsFor(highWater)
	if options == nil {
		options = map[string]any{}
	}
	options["num_predict"] = 1
	// The one deliberate difference: with ollama_num_ctx unset a chat request
	// defers to the server, but loading the model that way sizes the runner to
	// the tag's advertised window (262144 on qwen3.8) and pins the RAM. Warmup
	// floors it instead. The two then disagree and the first real turn reloads
	// — pinned by TestWarmupMatchesChatOptions/window_unset_diverges, which is
	// where to start if that reload is ever worth closing.
	options["num_ctx"] = o.warmupNumCtx(highWater)
	req := o.chatRequest(messages, tools, options)
	stream := false
	req.Stream = &stream
	fmt.Fprintf(os.Stderr, "obey-voice: warmup model=%s num_ctx=%v num_batch=%v prefix_chars=%d tools=%d\n", o.model, options["num_ctx"], options["num_batch"], len(messages[0].Content), len(tools))
	o.sendWarmup(ctx, req)
}

func (o *OllamaBrain) sendWarmup(ctx context.Context, req *api.ChatRequest) {
	if o.turnStarted.Load() {
		fmt.Fprintln(os.Stderr, "obey-voice: warmup skipped: a turn reached the model first")
		return
	}
	_ = o.chat(withModelRequestPurpose(ctx, PurposeWarmup), req, func(api.ChatResponse) error { return nil })
}
