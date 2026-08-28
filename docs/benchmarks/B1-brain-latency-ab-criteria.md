# B1 — conversational brain model A/B: criteria

**WI-a981e4. Written before any measurement was taken.** The gate below is the
contract; the numbers arrive afterwards and are judged against it. If the
criteria are wrong, they get argued with here — not retrofitted to whatever the
run produced.

## Why this exists

On 2026-08-28 Lance's live voice sessions on `qwen3.8-uncensored:27b` produced:

- **27.8 s to first token** on a conversational turn, and
- a **227.8 s turn** that died as `model timed out before finishing a reply`.

That second one is not a slow reply, it is a *lost* reply: the pipeline wraps
every turn in `defaultBrainTurnTimeout = 3 * time.Minute`
(`pkg/voiceagent/pipeline/pipeline.go:126`, applied at `:714` and `:802`, and the
error text is raised at `:744` / `:833`). A 227.8 s turn blew a 180 s deadline,
so the user heard the recovery line instead of an answer.

Persona brain-model defaults are currently set by taste. This benchmark exists so
the next default is set by measurement.

## What is measured

The **real brain seam**, not the raw Ollama HTTP API:

- `brain.NewOllama(cfg)` built from Lance's live config (`config.Load()`), with
  the `obey` persona profile applied through `persona.Apply` exactly as the app
  applies it.
- Turns driven through `OllamaBrain.ThinkStream(ctx, input, StreamOptions{...})`
  with `VoiceMode: true` and `ToolsEnabled` taken from `voice_tools_enabled`.
- Therefore the measured prompt includes, byte for byte, everything the model
  really sees: persona document, environment grounding block, the Agent Skills
  catalog, and the turn instruction (`brain.AssembleSystemPrompt`, policy in
  `pkg/voiceagent/brain/policy.go`), plus the tool schemas.
- Therefore the measured latency also includes the per-turn semantic skill-router
  embedding call (`routeSkillContext`), which runs synchronously before the chat
  request. It is timed separately and reported, because it is a real component of
  the user's wait.
- Request options are the ones `newChatRequest` actually sets: `num_ctx` from
  `ollama_num_ctx`, `think` from `ollama_think`, `keep_alive` from
  `ollama_keep_alive`, `stream: true`. The effective options are recorded in the
  artifact.

Timing definitions, from the caller's point of view:

| Name | From | To |
|---|---|---|
| `route_ms` | `ThinkStream` called | `ThinkStream` returns (skill routing done) |
| `ttft_ms` | `ThinkStream` called | **first streamed chunk** |
| `total_ms` | `ThinkStream` called | stream closed |
| `gen_tok_s` | — | `eval_count / (total_ms - ttft_ms)` |
| `prefill_tok_s` | — | `prompt_eval_count / ttft_ms` |

`ttft_ms` is the number that decides whether the agent feels alive, so it is
measured from the call, not from the first HTTP byte. Prefill and generation
token counts come from Ollama's own metrics via the `OnUsage` hook, not from an
estimate.

## The gate

A model may be a **conversational default** for a persona only if, measured warm
on this machine, it clears all of:

| ID | Criterion | Threshold |
|---|---|---|
| **G1** | First-token p50 | ≤ **2.0 s** |
| **G2** | First-token p95 | ≤ **4.0 s** |
| **G3** | Generation throughput, median | ≥ **25 tok/s** |
| **G4** | Worst sampled full turn | ≤ **90 s** (half the 180 s pipeline turn timeout) |
| **G5** | G1–G4 hold in **both** history conditions | empty **and** ~6k-token filled |

**G4 is a headroom rule, not a timeout rule.** A model that merely stays under
180 s is one long reply away from the failure Lance already hit. Requiring 2×
headroom on a 3-sentence reply is the point.

**G5 is the one most likely to bite.** `ollama_num_ctx` is 8192. A real
conversation fills that. Measuring only at empty history measures a condition the
user is almost never in, which is why both conditions are mandatory and why a
pass in one and a fail in the other is a **fail**.

### Reported but not gated

- **Cold load** (first request after `ollama stop`). `keep_alive: 10m` means this
  is paid once per idle gap, not per turn. It is recorded because a 40 s cold
  load is still a real experience, and because it bounds how many models can
  plausibly stay resident.
- **`route_ms`** (skill-router embedding). Same for every candidate; it shifts
  the whole field, it does not rank it.
- **Prefill throughput.** Diagnostic: it explains *why* a model fails G5 when it
  passes G1.

### Statistics, stated honestly

15 samples per model per condition (5 prompts × 3 warm reps). At n=15 a "p95" is
the nearest-rank 95th percentile — effectively the worst or second-worst
observation. **It is reported as `p95` but it should be read as "worst observed",
and it is not a distributional claim.** Medians across 15 samples are the number
to trust. Reps are serialized, one model at a time, on a machine that is also
Lance's daily driver; contention is a real error term and no confidence interval
here would be honest.

## Conditions

**Warm.** Model already resident. One discarded warm-up generation precedes the
measured reps, so no measured sample pays a load.

**Empty history.** `ClearHistory()`. Prompt = system prompt + tool schemas + one
user turn.

**Filled history (~6k tokens).** A realistic prior conversation is loaded via
`LoadHistory` before each rep — actual dialogue about this project's work, not
lorem. It is sized against the *measured* system-prompt length so the total
estimated prompt lands near 6,000 tokens: comfortably inside `num_ctx: 8192`, and
below the 90 % trim line (`ensureContextBudget`, 7,372 tokens) so
`trimHistory`/budget-trimming never fires mid-measurement and silently changes
the population. The harness records the estimated prompt tokens and asserts no
trim occurred.

History is reset to the condition's baseline between every rep, so reps are
independent samples of the same prompt size rather than a growing conversation.

## Candidates

Only models **already installed** are measured. Nothing is pulled; nothing is
deleted. The install list is recorded in the artifact.

| Model | Why it is in |
|---|---|
| `qwen3.8-uncensored:27b` | **Incumbent.** `ollama_model` in the live config, and pinned by the `veronica` persona. The model that produced the 27.8 s and 227.8 s turns. |
| `qwen3.8:latest` | Today's persona-pinned default for **`obey`** and `samantha`. Different weights from the uncensored tag (different digest), same size class. The head-to-head that matters most for this WI. |
| `qwen3:30b` | MoE (30B-A3B). ~3B active parameters per token: the central speed hypothesis — large-model quality at small-model token rate. |
| `kimi-k2.6-distilled:IQ4_XS` | Second MoE (35B-A3B distill), already persona-pinned (`major-dickhead`). Tests whether the MoE result generalizes or is a qwen artifact. |
| `gemma4:26b` | Dense 26B, persona-pinned (`dylan`). A non-qwen dense control in the incumbent's size class. |
| `qwen3:8b` | Dense 8B. The obvious "just use a smaller one" answer; measured rather than assumed. |
| `dolphin-mistral:latest` | Dense ~7B, **uncensored**. The incumbent was chosen partly for being uncensored, so a fast uncensored option is decision-relevant, not just a size point. |
| `llama3.2:latest` | Dense 3B. Speed floor — establishes the best case this machine can do, so every other result reads against a known bound. |
| `llama3.3:latest` | Dense 70B, 42 GB. Named in the brief. Measured to close the question rather than dismissed; run last so its footprint cannot perturb the others. |

**Excluded, with reasons:**

- `nemotron3:33b`, `nemotron-3-nano:30b`, `qwen3.6:latest`, `qwen3-coder:latest`,
  `lfm2:latest` — same size class as candidates already covered, or
  coder-specialized; adding them lengthens a serialized run on a live machine
  without changing the decision.
- `deepseek-v4-flash:cloud` — cloud-hosted. Measuring it would answer a different
  question than "what should the local default be", and the local-first premise
  is the product.
- `smollm2:135m`, `phi4-mini-reasoning`, `local-phi-3.1`, `local-llama-3.1`,
  `llama2` — obsolete or too small to hold a persona.
- `nomic-embed-text` — the skill router's embedding model, not a brain. It is
  *used* by every measurement.
- `qwen3.8-uncensored:latest`, `orcarouter/Qwen3.8-27B-Uncensored:q4_K_M` — same
  digest as `qwen3.8-uncensored:27b`. Measuring an alias measures nothing new.

Any excluded model can be measured by re-running the harness with `-models`.

## What this does NOT measure

**Response quality.** This is a latency benchmark. Nothing here says a model is a
good conversational partner, funny, in-persona, or correct. That is Lance's
judgment in real use, and no number in the artifact substitutes for it.

The harness records each candidate's actual reply text and a single
**coherence pass/fail eyeball** — did it answer the question asked, in speakable
prose. That is a floor check to catch a model that is fast because it is
producing garbage. It is deliberately not a score, because a score here would be
theater.

**A default flip.** This PR changes no behavior. It produces a recommendation.
Lance decides.

## Machine discipline

This runs on Lance's live machine against his live Ollama server.

- Everything serialized: one model loaded at a time.
- Previous candidate explicitly stopped before the next loads, so cold-load
  numbers are real and resident memory never stacks.
- `~/.obey` is **read only**. No config is written. The persona overlay is applied
  to an in-memory copy.
- No model is pulled or deleted.
- On completion the incumbent (`qwen3.8-uncensored:27b`) is re-warmed with one
  short generation, so Lance's next conversation does not pay a cold load caused
  by this benchmark.

## Success for the WI

The WI succeeds if it can state, with committed numbers behind it:

1. which model should be the `obey` persona's conversational default,
2. whether `qwen3.8-uncensored:27b` earns a slot as a deliberate *depth* persona
   rather than a conversational one, and
3. whether `ollama_num_ctx` or `defaultBrainTurnTimeout` deserve tuning.

It succeeds even if the answer is "the incumbent is fine" — provided the numbers
say so.
