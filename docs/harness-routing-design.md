# Harness routing and worker capability parity

Status: proposed design; runtime implementation pending. Workitem: WI-da43b6.
Source reviewed: `892f899cff1695d86c85c8b9d3971b7f4042c473`.

An ordinary request such as “Can you tell me what the weather is today?” should
reach an enabled search-capable worker regardless of the selected chat harness.
The assistant should distinguish its direct tools from work the application can
dispatch, preserve conversation constraints, and return a sourced result or a
specific failure. File operations, shell tasks, and image retrieval need the
same reliable dispatch boundary.

This document specifies the proposed fix and acceptance gates. It does not claim
that routing parity or cross-provider runtime verification is complete.

## Confirmed failure and execution path

A Codex-backed persona denied weather and web lookup while the application had
`voice_tools_enabled: true`. Correlated runtime diagnostics showed `tools=false`
for the live turns, and the saved conversation contained no search or worker
dispatch. Private session details remain outside this repository.

1. [`voice_turn.go`](../pkg/voiceagent/pipeline/voice_turn.go) and
   [`text_turn.go`](../pkg/voiceagent/pipeline/text_turn.go) try deterministic
   routing before invoking the live brain.
2. [`route.go`](../pkg/voiceagent/pipeline/route.go) recognizes narrow action
   prefixes such as “search the web”. Ordinary weather questions do not match.
   Prefix-based spawn is also skipped for tiered workers, which need tier data.
3. [`decision.go`](../pkg/voiceagent/pipeline/decision.go) can accept a ready
   proposal from the optional decision feature. This feature is gated, limited
   to eligible tiered requests, and never awaited. It cannot guarantee routing.
4. [`liveCLITools`](../pkg/voiceagent/brain/stream_options.go) returns false for
   the default live `route_only` policy even when turn tools are enabled.
5. [`codex.go`](../pkg/voiceagent/brain/codex.go) uses that gate;
   [`codex_policy.go`](../pkg/voiceagent/brain/codex_policy.go) explicitly disables
   web search and integrations on tools-off requests. The conversational answer
   does not pass through an Ollama-style route tool loop.

Search itself exists: [`tools.go`](../pkg/voiceagent/brain/tools.go) registers
`web_search` and `fetch_url`, and [`web_tools.go`](../pkg/voiceagent/brain/web_tools.go)
implements DuckDuckGo HTML search. Existence of a worker tool does not prove
that a particular worker or network connection can execute it successfully.

## Provider scope

| Provider | Inspected live behavior | Evidence and verification limit |
| --- | --- | --- |
| Ollama | Exposes and interprets `route`, with missing-route recovery | `router_tools.go`, `ollama_stream.go`, `ollama_blocking.go`, `ollama_route_recovery.go`; control implementation, not a claim of perfect routing |
| Codex | Shared gate disables native tools and web search | `codex.go`, `codex_policy.go`; reported runtime failure confirmed |
| Claude | Shared gate feeds native tools-off options | `brain.go:runOptions`, streaming and full paths; runtime parity unverified |
| Cursor | Shared gate selects restricted ask options | `cursor.go:askOptions`; runtime parity unverified |
| Grok | Shared gate supplies an empty native tool list | `grok.go:runOptions`; runtime parity unverified |
| Muse Spark | Shared gate disables write, shell, and web tools | `muse_spark.go:museRunOptions`; runtime parity unverified |
| Obey daemon | Shared gate selects discussion mode; tools-off requires daemon policy | `obey.go:runOptions`; daemon permissions remain authoritative; runtime parity unverified |

Table source paths are under `pkg/voiceagent/brain/`. These adapters share an
admission gap, not necessarily identical native tool availability. Worker tools
are provider-specific; do not advertise Ollama tool names to native harnesses.

[`ToolSurfaceApplies`](../pkg/voiceagent/brain/policy.go) currently sends routing
guidance only to Ollama. Its rationale assumes native harness tools, while the
live policy disables those tools. Capability explanations need to reflect the
effective turn and worker state rather than that assumption.

## Proposed shared contract

Keep work on workers. Add a host-owned routing step for eligible live turns whose
provider cannot return Obey Voice route decisions. Enabling every native tool in
the talking session or changing only a persona prompt does not satisfy this
design.

### Admission and routing

Resolve effective persona/provider, turn kind, policy, and worker availability
before routing. Distinguish live route support from native worker tool support;
extend existing provider metadata/interfaces narrowly rather than adding a
second provider registry.

Preserve deterministic controls. For eligible harness turns without a terminal
deterministic decision, obtain one bounded structured decision before emitting
the conversational answer. Reuse the route schema, validation, tier resolution,
and existing `completeRoutedTurn`/Agency path. Extract reusable parts of
Ollama's structured route recovery where appropriate instead of creating a
second chat/spawn protocol.

The routing backend must be explicitly configured and qualified. The
implementation must select its backend contract from installed capabilities
and existing configuration; not every harness has demonstrated schema-constrained
output support. Do not introduce an implicit paid provider or mandatory Ollama
dependency. If required routing is unsupported, surface that specific capability
failure rather than accepting a silently broken configuration.

The optional [decision feature](decision-routing.md) can supply a validated
proposal, but its disabled, not-ready, ineligible, or long-history state must not
bypass the correctness fallback. Preserve its current nonblocking optimization
policy and establish one decision consumer and dispatch owner.

Include bounded conversation context sufficient to resolve follow-ups,
locations, dates, and prior constraints. Validate worker tier through existing
admissibility rules. Chat starts the ordinary tools-off answer; spawn admits one
worker. Narration and control commands retain their owning implementations.
Assistant promises and denials are neither authorization nor structured routes.

### Capability guidance and delivery

Render concise host guidance from resolved state: which tools the live model
can invoke directly, which actions it can request through workers, and what is
disabled or unavailable. Advertise only capabilities supported by an admissible
worker plan. Static admissibility is not evidence of a successful live lookup.

Keep saved prompt overrides compatible with updated defaults. Updating embedded
instructions alone does not update user overrides; old persona assertions of
“full access” cannot be the authority for tool availability.

Retain worker history isolation and canonical result handoff, sources, limits,
and chat-media attachments. Acknowledge work after admission and claim success
only after worker evidence. A search title alone does not verify a forecast or
deliver a requested image.

### Failure, cancellation, and policy

Distinguish routing failure, unavailable worker, policy denial, and failed lookup.
None should collapse into a blanket “this app has no web search” claim. Ask a
useful clarification for missing essential parameters rather than inventing
locations or repeatedly dispatching an underspecified task.

Cancellation, timeout, persona/session retirement, and duplicate callbacks must
not start late work or deliver stale results. Preserve budgets, permissions,
network sandbox settings, remote default-deny, and tools-off restrictions.
Daemon permissions remain enforced by the Obey adapter.

Peer replies, discussions, continuations, and summary turns must not initiate
new user work. Native audio without a usable transcript retains its explicit
admission contract; do not secretly add transcription or route empty text.
Preserve Ollama route-only and legacy comparison paths independently.

Record correlated routing source, effective provider/policy, decision outcome,
worker tier/provider, job admission, and terminal result using existing events
and trace ownership. Avoid sensitive prompt text and duplicate terminal events.

## Implementation sequence

1. Pin the reported failure with isolated Codex runner and pipeline fixtures for
   typed and synthetic transcribed voice input. Audit streaming/full adapters,
   effective persona overrides, tools-off behavior, and worker capabilities for
   every provider above. Record the routing backend/configuration choice.
2. Implement shared harness admission, reusing route validation and Agency
   dispatch for legacy and tiered worker modes. Integrate optional proposals
   without duplicate consumption. Bound context, timeout, output, and retries;
   reject malformed decisions and unavailable tiers without speaking private JSON.
3. Align capability guidance, saved prompt handling, and diagnostics. Verify
   policy-compatible session reuse across persona/provider changes and reload.
4. Run the matrix below through provider-parametrized fixtures, then qualify
   available real harnesses independently. A fake runner or missing CLI/login
   does not establish a runtime pass.

## Acceptance matrix

| Case | Required observable evidence |
| --- | --- |
| Natural weather question with known location | Exactly one admitted worker; sourced result reaches saved transcript; no app-wide web denial |
| Missing essential location | Useful clarification or justified conversation context; no invented location/result |
| “What about tomorrow?” after weather lookup | Relevant date/location preserved in one new task |
| “What about web search?” after denial | Accurate capability explanation; dispatch when context makes a lookup request clear |
| Explicit “search the web for …” | Existing deterministic path preserved, no duplicate spawn |
| Greeting, identity, explanation of rain | Chat answer, no worker job |
| File, shell, or image action | Same dispatch contract; action/artifact evidence before success claim |
| Tools off or remote policy denial | No native invocation or worker-dispatch bypass; accurate explanation |
| No admissible worker or routing backend | Specific unavailability, no fabricated search or promise |
| Invalid decision/tier or routing timeout | No invalid job; explicit terminal routing outcome |
| Optional decision off/not ready/long history | Correctness fallback works and preserves relevant constraints |
| Failed network/search or empty results | Specific failed/unverified lookup outcome; no invented conditions |
| Cancellation or persona/session retirement | No late dispatch or stale delivery; one terminal outcome |
| Persona override and session reload | Effective provider controls routing, not global default; compatible native session policy |
| Peer/discussion/continuation/summary | No newly inferred user work; tools-off semantics preserved |
| Native audio without usable transcript | Explicit supported/unsupported admission preserved; no hidden STT fallback |
| Ollama route-only and legacy | Existing tool behavior, history isolation, and delivery preserved |
| Every harness listed above | Independent adapter check; real run qualified or explicitly marked unverified/unsupported |

Use disposable configs/sessions and quiet text input for real lookup checks.
Persist admitted constraints, live/worker policies, job lifecycle, and sourced
result. Verify acknowledgement and final result in the actual terminal/UI host;
a passing build alone does not prove this outcome. Hardware acceptance remains
opt-in.

Start with focused brain/pipeline tests, then the existing `just test unit`
runner. Extend current convoflow routing scenarios with natural current-information
requests and harness fixtures. Follow contribution checks for the eventual
runtime PR; record exact installed versions/models and provider-specific limits.
