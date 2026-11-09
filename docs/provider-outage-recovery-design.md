# Provider outage recovery

Status: proposed design; runtime behavior is unchanged by this document.
Tracking: campaign work item **WI-e6b508**.
Baseline: `3ea7e96912dab093900c45d75874e6c8eecda506`.

## Problem and outcome

An Ollama server can answer metadata requests while its model workers fail.
During the observed incident, both `/v1/audio/speech` and `/api/chat` returned
HTTP 500 with `mlx runner exited unexpectedly: signal: broken pipe`.
Speech recovered through Kokoro; brain turns produced no answer. The UI resumed
“Listening” with healthy audio devices, leaving the operator to interpret raw
errors and discover that the model backend needed attention.

The desired outcome is that a user can distinguish a healthy microphone from
an unavailable model, understand whether their request was answered, and resume
the conversation after recovery without duplicate messages or tool effects.

## Existing behavior to preserve

- `pkg/voiceagent/pipeline/speech_recovery.go` reports the real failure,
  preserves genuine partial text, and completes the turn as degraded so input
  remains available. Recovery text is never synthesized as persona speech or
  inserted into assistant history. See [Turn recovery](turn-recovery.md).
- `pkg/voiceagent/pipeline/synthesis.go` switches eligible speech failures to
  the configured Kokoro fallback and keeps that voice for the rest of the reply.
  A fallback success does not prove the primary speech provider recovered.
- `pkg/voiceagent/brain/ollama_request.go` already owns request accounting and
  bounded compatibility/tool-parse repairs. Extend that boundary rather than
  introducing a second HTTP client or retrying an entire tool-using turn.
- `internal/tui/conversation_events.go` retains errors in the transcript and
  Activity, but transient listening/progress status can replace the error panel.
- Existing response envelopes, utterance IDs, session boundaries, frozen persona
  bindings, and cancellation fences continue to own attribution and lifetime.

## Scope and decisions

Implement in ordered slices: provider failure classification and safe request
retry; persistent status; explicit failed-turn retry. Ollama is the first brain
adapter. Other providers retain their behavior until they supply equivalent
typed classification. The health representation must support both brain and
speech roles without conflating them with capture/playback devices.

Keep failures text-only. Do not add a spoken apology, error announcement, or
automatic brain-provider substitution. Do not restart, upgrade, unload, or
reconfigure an external Ollama instance from recovery code. It can serve other
applications and may run on another computer. Recovery does not change model,
persona, voice, context limits, tool policy, or request budgets.

### 1. Classify the failure at the provider boundary

Introduce a narrow typed error/description in the existing brain package with
provider ID, failure kind, retry eligibility, operator action, and wrapped cause.
Preserve `errors.Is`/`errors.As`; retain the original error in diagnostics.
Do not expose credentials, bearer tokens, request bodies, or full endpoint URLs
in status text. Identify the configured host with sanitized display metadata.

Distinguish worker exit, temporary service refusal, connection unavailability,
authentication/configuration errors, malformed output, cancellation, and exhausted
budgets. Use HTTP status and transport types where available. Limit message
matching to known Ollama worker-exit signatures, including errors carried inside
an otherwise successful NDJSON response. “Broken pipe” is an observed worker
termination; the client cannot infer its underlying operating-system cause.

Cancellation, budget refusal, malformed tool syntax, invalid input, and rejected
credentials must not enter the provider-outage retry branch. Existing tool-parse
and unsupported-option repairs retain their own rules and bounded accounting.

### 2. Retry only an undelivered model request

Allow at most one outage retry for a failed model request, after a cancellable
250 ms delay. Both attempts use the same caller deadline and existing request,
retry, token, and discussion budgets. Do not reset them or create a new logical
user turn. Warmup, worker, and foreground callers must not gain unbounded retries
by nesting this policy with existing compatibility repairs.

A retry is eligible only if the provider classified a known temporary failure,
the caller remains active, and the request delivered no content, thinking, tool
calls, or terminal completion. Callback failures are never transport retries.
Charge the failed attempt and reserve the retry before waiting/dispatching.

Retry the current model request, never the surrounding stream/tool loop. If a
prior request in the same turn already ran a tool, its recorded result stays in
the request history; retrying a later undelivered generation must not execute
that tool again. Any ambiguous tool execution or partial delivery disables
automatic replay. Reuse the existing delivery gate and budget helpers rather
than estimating safety from whether an assistant bubble is visible.

Publish a bounded “Ollama model unavailable; retrying once” operator status.
If the second attempt fails, finish through the existing degraded-turn path.
Avoid repeated automatic attempts on every idle listening tick.

### 3. Keep provider health visible independently of audio state

Represent health by effective provider binding and role: provider, normalized
endpoint identity, selected model, and brain/speech role. The same host can have
healthy text generation and failed speech, or the reverse. A successful small
model must not mark a different failing model healthy.

Use a bounded session-owned projection with `unknown`, `retrying`, `unavailable`,
and `available` states. Follow the existing event/snapshot conventions. Include
response attribution and a binding/generation fence so late events from retired
providers, cleared conversations, or another persona cannot overwrite current
status. All supported clients must either render the event or retain the
existing raw error fallback; do not silently drop failures during migration.

Example persistent status after retry exhaustion:

> Brain unavailable: Ollama's model worker failed. Check or restart Ollama on
> its host, then retry the request.

The TUI must show this alongside listening/audio-device state; listening ticks,
meter updates, and successful Kokoro fallback must not erase it. Keep raw errors
in the existing transcript/Activity/log surfaces. A successful primary inference
for the same binding clears the outage and records a recovered transition once.
Returning to listening, answering `/api/version`, or listing models is not a
recovery signal. A successful inference in an incomplete stream is also not
proof of completed model recovery.

Do not add an inference polling loop: normal requests establish recovery. If a
future explicit “Check provider” action performs inference, label it as an
inference request and obey the selected endpoint, budgets, and cancellation.

### 4. Offer an explicit retry for the failed request

Keep a bounded retry descriptor referencing the existing logical input, failed
response, frozen persona/provider binding, and session epoch. Do not create a
parallel conversation store or deduplicate user messages by their text.

Initially offer the action only for a terminal failed request with no delivered
answer and no tool execution/uncertain effects. Retry reuses the existing user
turn in persisted and provider history, while recording a distinct response
attempt. The action must enter through the owning turn driver, stop/drain an
active listening capture normally, and reject double activation while pending.

Retire the action after a newer user input, explicit cancellation, conversation
clear/new session, loss of required audio attachments, or provider/persona
rebinding. Selecting another persona must never retry the older input under that
persona. Reloading a session preserves the failed request and diagnostics; the
initial implementation need not restore a live retry action after process exit.

Partial answers remain visible and incomplete. Turns with completed/uncertain
tool effects require a new explicit user instruction; they do not expose the
simple replay action. An automatic request retry and an explicit user retry must
not append the same input again or duplicate a tool result.

## Implementation order and ownership

1. Add typed Ollama outage classification and request-local retry in
   `pkg/voiceagent/brain`; prove delivery, accounting, cancellation, and tool
   boundaries with controlled HTTP/NDJSON fixtures.
2. Extend the existing event attribution/export and runtime projection for
   provider status. Inspect `pkg/voiceagent/events`, persona runtime ownership,
   TUI bridge durability, network event serialization, desktop bridge, and the
   Apple client's compatibility before adding a new event contract.
3. Render persistent status through `internal/tui` and the desktop event path.
   Retain the current raw errors and text-only recovery contract. Follow the
   desktop directory's own instructions when implementing there.
4. Add explicit retry through the existing turn driver and canonical history
   path. Reuse the conversation/request identity work from **WI-bbc8e9** if it
   supplies the needed logical request ledger; do not build a competing ledger.
   If that dependency is not ready, ship slices 1–3 and keep replay disabled.
5. Update [Turn recovery](turn-recovery.md), configuration/help only where
   behavior changes, and the work-item handoff with measured evidence.

The design PR does not claim these slices are implemented. Each implementation
PR must name its delivered slice and remaining acceptance cases.

## Acceptance evidence

| Case | Required result |
|---|---|
| First model request returns worker-exit HTTP 500, second succeeds | Exactly two requests, one input/history entry, one answer; recovered status for the same binding. |
| Worker-exit error arrives in NDJSON before output | Same bounded recovery as a pre-output HTTP failure. |
| Both attempts fail | One retry maximum, durable actionable outage status, real diagnostics, live input, no fabricated assistant reply. |
| `/api/version` succeeds while generation fails | Status remains unavailable. |
| Failure follows text, thinking, a tool-call delta, or completion | No automatic replay; preserve partial output and existing incomplete disposition. |
| Prior tool result exists before a later undelivered generation fails | Any retry is of that generation only; tool execution count and stored result remain unchanged. |
| Cancellation during delay/request; callback failure | No retry after cancellation, prompt return, one terminal outcome, no fallback error speech. |
| Authentication/invalid input/budget exhaustion | No outage retry; correct actionable error and unchanged limits. |
| Compatibility repair combines with outage retry | Explicit finite accounting; no retry multiplication or reset of budgets. |
| Primary TTS fails, Kokoro succeeds | Reply continues; primary speech health remains unavailable; brain/audio-device health unaffected. |
| Listening ticks and audio meters follow brain failure | Persistent provider warning remains visible in an actual terminal/desktop flow. |
| Another model/persona succeeds or late old-binding event arrives | Failing current binding is not incorrectly marked recovered. |
| Explicit retry twice, then session is saved/reloaded | At most one pending retry; one canonical user input and no duplicated assistant/tool records. |
| New input, clear, session change, persona change, missing attachment | Old retry action is invalidated without dispatch. |
| Tool effects or partial answer make replay unsafe | No simple replay action; preserve evidence and accept a new instruction. |

Use isolated HTTP fixtures and disposable config/session roots. Run focused
race-enabled tests for the changed owners, then `go test ./...`; use `just build`
for runtime/startup changes and the desktop gates when desktop files change.
Verify the actual TUI with the repository's terminal harness and inspect saved
history. Do not stop or corrupt a user's Ollama instance to reproduce an outage.
Live-model checks are optional supporting evidence, not a replacement for the
negative cases above. A design-only PR checks links and source references and
does not claim runtime or UI acceptance.
