# Optional decision routing

This package lets a small decision model propose a worker tier while the voice
pipeline performs its existing input admission. It also answers one blocking
question for group conversations, "who is being addressed?" (`AddresseeRequest`,
`InterpretAddressee`, `Controller.ResolveAddressee`), which uses the same
contract and validation rules as the Ollama addressee classifier. A ready, valid proposal can skip
the speaking model's routing call. The feature is off by default.

`Evaluator` is the replaceable transport boundary. `SystemOne` supports local
Decider and explicit Jev selection. Local transport requires loopback HTTP,
disables proxies, and rejects redirects. Jev requires its official HTTPS origin,
an explicit model and key. `OpenAI` uses the Responses API with a strict categorical
schema; it does not manufacture confidence scores. Providers never retry or
switch to cloud automatically.

`WorkPolicy` interprets bounded input, versioned questions and enabled tiers.
`route_choice` is the application's default experiment; `intent_and_tier` retains
the original two-question policy. Local/Jev proposals require a complete valid
probability distribution, confidence and margin. OpenAI proposals use a separate
categorical acceptance rule. These are experimental policies, not calibrated
correctness guarantees. A proposal cannot choose an executable command, worker
model, endpoint, permission or spending authorization.

`Controller` owns independent `voice_work` and `text_work` points, an
`addressee` point, configuration generations and at most one in-flight work
evaluation plus one in-flight addressee evaluation. `Start` copies the input and
runs asynchronously. `Attempt.Take` checks once, never waits, and cancels unfinished
inference. Late, cancelled and old-generation results cannot be adopted. `ResolveAddressee`
is the one call that waits, for at most the budget; it reports transport,
timeout and invalid-output failures as admission classifier failures, and returns
no answer (not an error) when off, busy, abstaining or reconfigured.

`Manager` prepares a provider asynchronously, exposing off/preparing/ready/
unavailable status. The controller stays off until preparation succeeds. Managed
local processes are owned by `pkg/voiceagent/decider`; disabling or changing
settings cancels their setup and inference and closes the previous process.

The owning pipeline preserves deterministic commands and skips background turns,
continuations, active jobs and narration. It fences input rewrites, conversation,
brain, canonical history and worker settings. Accepted proposals still go through
ordinary Agency admission, queueing, acknowledgement and history recording.

No work-routing boundary waits for the optional model; only addressee resolution does, and only up to the budget. Shared CPU/GPU contention can
still affect latency; measure on the intended hardware. See
[setup, pipeline diagrams and replay instructions](../../../docs/decision-routing.md).
