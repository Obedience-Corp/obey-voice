# B2 — qwen3.8 fixed TTFT: mechanism criteria

**WI-adf58f. Written before any measurement was taken.** B1 established *that*
`qwen3.8-uncensored:27b` pays a ~4.3 s fixed per-turn cost before its first
spoken word. This document fixes, in advance, what evidence would name the
*mechanism* — and what evidence would rule each candidate out.

B1 lives on branch `bench/brain-latency-ab` (PR #62), not yet on `main`:
[`B2` is its follow-up, not its replacement](B2-qwen-ttft-mechanism-2026-08-28.md).

## Why this exists

B1's recommendation was "switch the default to `gemma4:26b`". That is a
*workaround*, not an answer, and it costs Lance the model he picked on quality.
This item exists to answer the question B1 explicitly left open — "what causes
it is **not established by this benchmark**" — so that the choice becomes
*informed* rather than forced:

- If the cost is a **configuration or prompt artifact**, `qwen3.8-uncensored:27b`
  keeps its quality *and* gets sub-1.5 s first tokens, and B1's recommendation
  is withdrawn.
- If the cost is **intrinsic** to the model under this runtime, that is a real
  finding and B1's recommendation stands on firmer ground than "it is slow".

Either outcome is a result. The failure mode this document exists to prevent is
declaring the first plausible story the winner.

## The target

`qwen3.8-uncensored:27b` reaching **TTFT ≤ 1.5 s** on a conversational turn, at
`num_ctx 8192`, warm, with the production tool schemas attached — measured the
same way B1 measured it (first byte of *speakable content*, not first byte of
anything).

"Speakable content" is the load-bearing half of that sentence. B1 already caught
`qwen3:30b` posting a 176 ms TTFT that was really its first `<think>` token
arriving in the `content` field. A mechanism story that only moves *first token*
without moving *first spoken word* has not fixed anything.

## Candidate mechanisms and their discriminating signatures

Each row is a hypothesis with a pre-committed signature. The measurement below
produces every column, so the table is decided by data rather than by argument.

| # | mechanism | signature that CONFIRMS it | signature that RULES IT OUT |
|---|---|---|---|
| M1 | **Hidden thinking** — the model generates a full reasoning block that `PARSER qwen3.5` routes to `message.thinking`, so `content` starts only after it ends | first `thinking` chunk arrives early (≈ prefill time); `thinking` streams for ~4 s; first `content` chunk at ~4.3 s; `thinking` field non-empty and ~100+ tokens | `thinking` empty/absent AND nothing arrives on any channel until ~4.3 s |
| M2 | **Suppressed thinking** — same generation, but discarded rather than surfaced | nothing streams until ~4.3 s, yet `eval_count` greatly exceeds the visible content token count | `eval_count` ≈ visible content tokens |
| M3 | **Prefill / prompt processing** — the 27B qwen35 simply processes the prompt 10× slower than gemma4 | `prompt_eval_duration` ≈ 4.3 s; per-token prefill rate ≈ 10× worse than gemma4 at equal prompt size | `prompt_eval_duration` ≪ 1 s |
| M4 | **Model load / runner churn** — the model is not actually resident between turns | `load_duration` is seconds, not milliseconds; `ollama ps` shows reloads | `load_duration` ≈ 0 on warm turns |
| M5 | **Vision projector cost** — the incumbent carries a 460 M-param CLIP projector that `gemma4:26b` does not report at all | cost scales with, or is only present alongside, projector engagement; text-only prompts unaffected would rule it out | text-only turns pay the full cost (a projector with no image to encode is not doing work) |
| M6 | **Tool-schema × template interaction** — the 820 prompt tokens of tool schemas trip something in the renderer | cost collapses with `tools` omitted | tools-off still leaves a multi-second floor (B1 already measured ~3.8 s remaining — this is a *verification*, not an open question) |
| M7 | **Sampler / first-token warm-up** — a per-request fixed setup cost independent of both prompt and generation | cost is invariant to prompt size *and* to think state, and is not accounted for by any of `load_duration`, `prompt_eval_duration`, `eval_duration` | the durations sum to the wall clock |

M1 and M2 are the primary hypotheses and are tested first. They are separated
only by whether the tokens are *surfaced*; both would mean the model is being
made to reason before every spoken word.

### Why M1/M2 are ranked first, stated honestly

This is a prior from reading the model metadata, not from data, and it is
recorded here so it can be wrong in public:

- `qwen3.8-uncensored:27b` and `qwen3.8:latest` both ship
  `RENDERER qwen3.5` / `PARSER qwen3.5` — prompt rendering compiled into
  Ollama, with a stub `TEMPLATE {{ .Prompt }}`.
- `qwen3:8b` (170 ms TTFT) ships a *legacy Go template* which, when
  `think` is set false, does two things the built-in renderer may not:
  it appends ` /no_think` to the last user message, **and** it prefills a
  closed `<think>\n\n</think>` block into the assistant turn — so the model
  never generates a reasoning block at all.
- `qwen3:30b` (176 ms TTFT, leaks raw `<think>` into speech) ships a template
  doing *neither*. Its fast TTFT and its leak are the same fact.

If the built-in `qwen3.5` renderer also does neither, the incumbent thinks on
every turn and the parser hides it. That would make B1's "fixed cost" and
qwen3:30b's "leak" the same phenomenon wearing different clothes.

**A prior is not a finding.** M3–M7 are measured on the same run regardless of
what M1/M2 show, and the artifact reports all of them.

## What is measured

One streaming request per sample against Ollama's `/api/chat`, recording:

**Timing (wall clock, from request send):**
- `first_any_ms` — first chunk carrying *anything*, on either channel
- `first_thinking_ms` — first chunk with non-empty `message.thinking`
- `first_content_ms` — first chunk with non-empty `message.content` *(this is TTFT; it is the number that must reach 1.5 s)*
- `total_ms` — final chunk

**Ollama's own accounting (final chunk):** `load_duration`,
`prompt_eval_count`, `prompt_eval_duration`, `eval_count`, `eval_duration`,
`total_duration`.

**Content:** full `content` and full `thinking` text, verbatim, plus a
`content_has_think_tag` flag for the qwen3:30b failure mode.

**Derived:** `hidden_token_ratio` = `eval_count` ÷ (tokens visible in
`content`) — the M2 discriminator; `prefill_tok_per_s` =
`prompt_eval_count` ÷ `prompt_eval_duration` — the M3 discriminator.

## Conditions

Every condition runs against the same prompt so the columns are comparable.

| condition | `think` field | prompt tweak | tools | tests |
|---|---|---|---|---|
| `think-false` | `false` (production setting) | — | on | baseline, M1/M2 |
| `think-true` | `true` | — | on | does the flag do *anything*? |
| `think-absent` | field omitted | — | on | is `false` different from default? |
| `no-think-marker` | `false` | ` /no_think` appended to user text | on | the one-line fix candidate |
| `think-false-notools` | `false` | — | **off** | M6 verification |
| `nothink-notools` | `false` | ` /no_think` appended | **off** | fix × M6 interaction |

Run on `qwen3.8-uncensored:27b` (the model that must be saved) and, as controls,
`gemma4:26b` (fast, same size, built-in renderer) and `qwen3:8b` (fast, legacy
template with the `/no_think` prefill). Controls run the `think-false` condition
only — they exist to calibrate, not to be optimized.

**n = 5 per model per condition**, plus one discarded warm-up sample per model.
Five is small; it is chosen because the effect B1 measured is enormous
(4637 ms vs 364 ms) and tight (p95 4824 ms), so five samples separate 4.3 s from
0.4 s with room to spare. It is *not* enough to resolve a 15 % difference, and
no claim in the artifact will rest on one.

## The gate — what counts as a fix

A candidate fix is only reported as a fix if, on `qwen3.8-uncensored:27b` with
tools on:

1. **`first_content_ms` p50 ≤ 1500 ms** — the actual goal.
2. **No `<think>` tag in `content`** on any sample — a fix that converts hidden
   thinking into *spoken* thinking is a regression, not a fix. This is the
   qwen3:30b trap.
3. **The reply is still a real answer** — content non-empty on every sample,
   and read by hand for quality. A model that answers faster because it stopped
   answering is a failure. Reply text is stored verbatim in the artifact so this
   is auditable rather than asserted.

If a fix passes 1–3 and is small and engine-side, it is implemented in this PR
behind the measurement. If it requires changing anything outside the repo
(Ollama version, a derived model tag, Lance's `~/.obey`), it is
**recommendation-only** — this item does not mutate the live machine.

If nothing passes, the honest finding is that the cost is intrinsic to
`qwen3.8-uncensored:27b` under Ollama 0.32.14, and B1's recommendation stands.

## What this does NOT measure

- **Reply quality.** Nothing here says the incumbent is better or worse than
  `gemma4:26b` at being Lance's assistant. That is the reason he picked it and
  it is not in scope.
- **Other machines.** One M4 Max, one Ollama build. A renderer bug would be
  reproducible elsewhere; a hardware-bound prefill limit would not.
- **Other models in the qwen3.8 family** beyond the two tags installed.
- **Whether Ollama upstream considers this a bug.** Out of scope, though the
  artifact should leave enough evidence to file one.

## Machine discipline

This runs against Lance's live Ollama on his working machine.

- **Serialized.** One model resident at a time; no concurrent requests.
- **Never `pull`, never `rm`, never `create`.** Only models already installed
  are touched, and no new tags are made.
- **`~/.obey` is never written.** The probe talks to Ollama directly and takes
  its model list from flags.
- **The incumbent is left warm.** On completion, `qwen3.8-uncensored:27b` is
  re-warmed with the production Warmup shape — `num_ctx 8192`, `num_predict 1`,
  `keep_alive 10m`, `think` false. A bare request would load it at the model
  default 262144 and pin RAM; PR #62's review documented that trap and this
  item does not re-step in it.

## Success for the WI

The mechanism named, with the row of the table that names it and the rows that
were eliminated; a fix implemented or a recommendation made or an honest
"intrinsic"; and Lance's machine left exactly as it was found.
