# B1 — conversational brain model A/B: results

**WI-a981e4 · 2026-08-28 · Apple M4 Max, 128 GB, macOS 26.5, Ollama 0.32.14**

- Gate, written first: [`B1-brain-latency-ab-criteria.md`](B1-brain-latency-ab-criteria.md)
- Raw artifact: [`B1-brain-latency-ab-2026-08-28.json`](B1-brain-latency-ab-2026-08-28.json)
- Harness: [`scripts/brain_bench`](../../scripts/brain_bench) — `just bench brain`

## Headline

The incumbent fails, and **not because it is large**. `gemma4:26b` is the same
17 GB on disk and answers **12× sooner**. The dead pause Lance hears is a fixed
per-turn cost specific to the qwen3.8 family on this machine — it is nearly
independent of how much context is in front of it, so shrinking the conversation
would not have fixed it either.

**Recommendation: make `gemma4:26b` the `obey` persona's conversational default.**
No default is flipped in this PR.

## Results

Warm, `n = 15` per model per condition. `empty` = fresh conversation;
`6k` = ~5.9k-token prior conversation. Gate: TTFT p50 ≤ 2.0 s, p95 ≤ 4.0 s,
≥ 25 tok/s, worst turn ≤ 90 s — **all four, in both conditions**.

| model | cond | TTFT p50 | TTFT p95 | tok/s | worst turn | gate |
|---|---|---:|---:|---:|---:|:--:|
| **qwen3.8-uncensored:27b** *(incumbent)* | empty | 4637 ms | 4824 ms | 23.9 | 8.8 s | **FAIL** |
| | 6k | 4733 ms | 5458 ms | 23.4 | 13.5 s | **FAIL** |
| **qwen3.8:latest** *(today's `obey` pin)* | empty | 4639 ms | 4861 ms | 11.3 | 13.8 s | **FAIL** |
| | 6k | 4897 ms | 23349 ms | 9.1 | 35.4 s | **FAIL** |
| qwen3:30b | empty | 176 ms | 219 ms | 111.8 | 22.4 s | pass\* |
| | 6k | 216 ms | 251 ms | 91.4 | 36.4 s | pass\* |
| kimi-k2.6-distilled:IQ4_XS | empty | 266 ms | 1390 ms | 94.3 | 1.8 s | pass |
| | 6k | 292 ms | 3213 ms | 89.2 | 3.5 s | pass |
| **gemma4:26b** | empty | **364 ms** | **382 ms** | 96.4 | **0.9 s** | **pass** |
| | 6k | **388 ms** | **423 ms** | 92.5 | **1.3 s** | **pass** |
| qwen3:8b | empty | 170 ms | 208 ms | 79.9 | 1.0 s | pass |
| | 6k | 199 ms | 239 ms | 73.2 | 1.9 s | pass |
| dolphin-mistral:latest | empty | 105 ms | 138 ms | 94.9 | 2.5 s | pass\* |
| | 6k | 137 ms | 200 ms | 86.2 | 2.9 s | pass\* |
| llama3.2:latest | empty | 894 ms | 1606 ms | 231.7 | 1.7 s | pass\* |
| | 6k | 1230 ms | 1770 ms | 174.6 | 3.5 s | pass\* |
| llama3.3:latest | empty | 6553 ms | 15170 ms | 11.2 | 19.3 s | **FAIL** |
| | 6k | 16486 ms | 18176 ms | 13.4 | 31.8 s | **FAIL** |

\* Passes the latency gate but is disqualified by behavior — see
[Three models pass the gate and still fail the job](#three-models-pass-the-gate-and-still-fail-the-job).

Zero errors and zero turn-timeouts across all 270 measured samples.

## The incumbent's problem is a fixed cost, not size and not context

Every candidate's TTFT is close to flat between a 1.4k-token prompt and a
5.9k-token one — the server's KV prefix cache is doing its job. What separates
the models is a **fixed per-turn cost**:

| model | disk | TTFT @1.4k tok | TTFT @5.9k tok | growth |
|---|---:|---:|---:|---:|
| qwen3.8-uncensored:27b | 17.7 GB | 4637 ms | 4733 ms | +2 % |
| qwen3.8:latest | 17.7 GB | 4639 ms | 4897 ms | +6 % |
| gemma4:26b | 18.0 GB | 364 ms | 388 ms | +7 % |
| qwen3:8b | 5.2 GB | 170 ms | 199 ms | +17 % |

Two models of essentially identical size, one 12× slower to speak, and the gap
does not come from context pressure. **The pre-registered hypothesis that
`num_ctx` was half of Lance's pain is wrong** — a 4.2× larger prompt costs the
incumbent 96 ms.

A diagnostic run isolates how little of it is ours to fix: disabling tools
removes 820 prompt tokens (the schemas) and buys back only ~830 ms, leaving a
**~3.8 s floor** on a 835-token prompt. Nothing in the config surface — tools,
context window, think — removes that floor.

What causes it is **not established by this benchmark**. It is a property of the
qwen3.8 family under Ollama 0.32.14 on this machine. The effect is large, tight
(p50 4637 ms, p95 4824 ms across 15 samples), and reproducible.

It also matches the complaint exactly. Lance's 27.8 s first token and 227.8 s
turn are this floor plus a longer reply — and 227.8 s is past the 180 s
`defaultBrainTurnTimeout` (`pipeline/pipeline.go:126`), which is why he got
"model timed out before finishing a reply" instead of an answer.

## Three models pass the gate and still fail the job

The gate measures latency. Reading the replies — which the artifact stores
verbatim — catches things latency cannot.

**`qwen3:30b` leaks its reasoning into speech.** Despite `think: "false"`, it
emits raw `<think>` blocks as ordinary message *content*:

> `<think>Okay, the user is asking for the capital of Australia. Let me think. I know that Australia's capital is Canberra. But wait, sometimes people confuse it with Sydney or Melbourne…`

`ollamaThinkValue("false")` sets the API's `think` field, which suppresses the
separate `thinking` field — it cannot suppress a model that writes `<think>` into
`content`. This is why qwen3:30b's median reply is **2,214 characters** and its
worst turn 36.4 s: the harness would have spoken all of it. Fastest TTFT in the
field, completely unusable for voice as configured.

**`llama3.3:latest` leaks tool-call JSON into speech** — `I'll call the
campaign-workitems skill to get insights on task management.{"name":
"read_skill", "parameters": …` — on top of failing the gate outright.

**`llama3.2:latest` called a tool on all 30 measured turns**, including
`web_search` for "what's the capital of Australia?". Every one is an extra model
round trip and an outbound HTTP request in the middle of a conversation.

**`dolphin-mistral`** stays clean and fast but answers in numbered lists
(`Here's an order you could follow:1. Review the pull request:…`), which is
markdown structure being read aloud.

### Unprompted side effects — worth Lance's attention on its own

Tool calls across the whole run, by model:

| model | calls |
|---|---|
| qwen3:30b | `web_search` ×4 |
| llama3.2:latest | `read_skill` ×21, `web_search` ×6, **`run_command` ×2**, `read_file` ×1 |
| llama3.3:latest | `web_search` ×15, `read_skill` ×6 |
| gemma4:26b, qwen3:8b, kimi, dolphin-mistral, both qwen3.8 | none |

`llama3.2` **ran shell commands** in response to plain conversation, and the run
left two files behind in the working directory that nobody asked for:

- `Hello.txt` containing `Hello` — from the warm-up turn *"Say hello in one short
  sentence."*
- `Email/Reply.txt` containing `My feedback on this pull request.` — from P4,
  which merely *mentions* replying to email and reviewing a pull request.

Both were deleted after the run. Neither was a measured-sample tool call, so they
came from the discarded warm-up turns, which is why they are not in the JSON.

This is not a latency finding, it is a **blast-radius** one: with
`voice_tools_enabled: true`, a weak model will write files and execute commands
in the agent's working directory because a sentence sounded vaguely like a
request. The four recommended-tier models did none of it — but the exposure is a
property of the configuration, not of the model that happens to be selected.
Worth a separate look at whether `write_file` and `run_command` should require
something stronger than a model's unassisted judgment on a voice turn.

`gemma4:26b`, `qwen3:8b` and `kimi` were clean on every sampled turn: no leaked
reasoning, no stray tool calls, replies scoped to the 2–3 sentences the turn
instruction asks for.

## Cold load (reported, never gated)

First turn after an unload. `keep_alive: 10m` means this is paid once per idle
gap, not per turn.

| model | disk | cold TTFT | cold total |
|---|---:|---:|---:|
| llama3.2:latest | 2.0 GB | 2.4 s | 2.5 s |
| dolphin-mistral:latest | 4.1 GB | 2.8 s | 2.9 s |
| qwen3:8b | 5.2 GB | 5.3 s | 5.4 s |
| qwen3.8-uncensored:27b | 17.7 GB | 9.2 s | 9.4 s |
| **gemma4:26b** | 18.0 GB | 10.7 s | 10.8 s |
| kimi-k2.6-distilled:IQ4_XS | 18.9 GB | 10.5 s | 10.5 s |
| qwen3.8:latest | 17.7 GB | 16.1 s | 17.4 s |
| qwen3:30b | 18.6 GB | 23.1 s | 28.3 s |
| llama3.3:latest | 42.5 GB | 36.9 s | 38.3 s |

Switching to `gemma4:26b` costs ~1.5 s more on a cold start and saves ~4.3 s on
**every warm turn after it**.

## Recommendation

### 1. `gemma4:26b` for the `obey` persona's conversational default

The strongest result in the field, and not by a small margin on the metric that
matters most for a voice agent — **worst observed turn of 1.3 s at full context**,
the tightest in the run.

- TTFT p50 388 ms / p95 423 ms at 6k context. A 45 ms spread between median and
  worst is a model that behaves the same way every time; the listener never
  learns to expect a pause.
- 92.5 tok/s — 4× the incumbent.
- Replies land at 216–249 characters: the 2–3 speakable sentences the turn
  instruction asks for, without being clipped.
- Clean on every sampled turn — no leaked reasoning, no unprompted tools, no
  markdown lists.
- **18.0 GB, against the incumbent's 17.7 GB.** No memory regression, and a 26B
  model rather than a downgrade to something small. It is already pinned by the
  `dylan` persona, so it is not an unknown quantity.

**Confidence: high** on the latency claim — the margin is 12×, the variance is
tiny, and it holds in both conditions. **Confidence: moderate** on it being the
right *overall* answer, because that depends on whether Lance likes talking to
it, which this benchmark cannot tell him.

`qwen3:8b` is the alternative worth trying if 18 GB resident is unwelcome: 199 ms
p50, clean replies, 5.2 GB. It gives up model size, not speed.

### 2. Yes — `qwen3.8-uncensored:27b` earns a *depth* persona slot

It is a bad conversationalist, not a bad model. 23.4 tok/s and a worst turn of
13.5 s are perfectly reasonable when a wait is expected and deliberate. Its
replies were the most naturally voiced in the field.

Two conditions on that:

- Use the **`qwen3.8-uncensored:27b`** tag, not `qwen3.8:latest`. Same 17.7 GB,
  but `:latest` generates at **9.1 tok/s** against the uncensored tag's 23.4, and
  its worst turn was 35.4 s. It is the weaker of the two on every measure here,
  and it is what the `obey` and `samantha` personas currently pin.
- A depth persona should carry a longer deadline explicitly rather than relying
  on the 180 s default it can plausibly exceed.

### 3. `num_ctx`: leave it at 8192 for now — but it is on the wrong layer

The pre-registered suspicion was wrong: context pressure is not what is hurting
Lance. Raising `num_ctx` with a fast model would be nearly free — gemma4 pays
24 ms going from 1.4k to 5.9k tokens — so once the default is fast, more
conversational memory is available cheaply. That is a follow-up to measure, not
something to change blind alongside a model swap.

The structural point is worth recording: **`ollama_num_ctx` is an app-level key
while the model is per-persona.** `internal/persona/overrides.go` routes
`ollama_model`, `brain_provider`, TTS keys and more — but not the context window.
So switching to a persona pinning a different-sized model changes half the pair
and leaves the other half behind. The right unit for that tradeoff is the
persona.

### 4. Leave `defaultBrainTurnTimeout` alone in this change

180 s was never approached by any passing model — worst case in the whole
recommended configuration is 1.3 s. The deadline only mattered because the model
was slow enough to reach it.

There is a real argument that 180 s is wrong for conversation: three minutes of
silence is not a wait, it is an abandonment, and a shorter deadline would let the
recovery line land while the user is still listening. But changing the model and
the deadline together makes it impossible to attribute the improvement. Pick the
model first.

## Method, in one paragraph

Every measurement goes through `brain.OllamaBrain.ThinkStream` — the same call
the voice pipeline makes — built from Lance's live config with the `obey` persona
overlay applied in memory. Each request carries the real assembled system prompt
(3,765 bytes: persona + environment grounding + Agent Skills catalog + turn
instruction, `sha256:4aed4325…`), the real tool schemas, and the real options:
`num_ctx: 8192`, `think: "false"`, `keep_alive: 10m`, `stream: true`. Token
counts come from Ollama's own metrics, not an estimate. The clock starts before
the synchronous skill-router embedding call, because the user is already waiting
by then.

Two conditions per model: empty history, and a 64-turn / ~5.9k-token filled
history of real prior dialogue. History resets to the condition baseline before
every sample, and each condition opens with a discarded warm-up on a prompt that
is never measured — without that, one sample per condition lands on a total
KV-cache hit and drags the median down.

**Config layering, recorded honestly.** `brain_provider` in the config file is
`grok`, but every persona profile pins `provider: ollama`, so the persona overlay
decides and the ollama path is what runs. That is also what Lance's session logs
show, and it is what was measured.

## Honest limits

**This measures latency, not quality.** Nothing here says whether any of these
models is good to talk to. That is Lance's judgment in real use. Every reply is
stored verbatim in the JSON; the coherence check above is a pass/fail read of
those replies, deliberately not a score.

**n=15 per model per condition.** The "p95" is a nearest-rank 95th percentile on
fifteen samples — read it as *worst observed*, not a distributional claim. The
medians are the numbers to trust.

**Live machine.** This ran on Lance's daily driver against his live Ollama
server. Contention is a real error term and no confidence interval here would be
honest. Runs were serialized, one model resident at a time.

**Tools stay enabled**, because `voice_tools_enabled: true` is what Lance runs.
The gate counts tool detours, since the user waits through them.
`clean_turns_summary` in the JSON gives the tool-free view.

**One deviation from the pre-registered plan.** The criteria said the filled
condition would be sized so the *estimated* prompt lands near 6,000 tokens. The
authored history tops out at ~5,934 estimated / ~5,835 real tokens, so the
condition is ~5.9k rather than exactly 6k. The estimator and the server agree to
within 2 %, and the conclusion does not turn on the difference — TTFT is flat
across this range for every candidate.

**`worktree_clean: false` in the artifact is not uncommitted code.** All harness
code was committed at `6f2e4fb` before the run. The dirty flag is the artifact
being written into `docs/benchmarks/` plus the two stray files `llama3.2` created
mid-run, both described above.

**Machine left as found.** `~/.obey` was read, never written. Nothing was pulled
or deleted; models were unloaded (`keep_alive: 0`) to measure cold load, which is
a load and not a removal. The incumbent was re-warmed at the end and confirmed
resident.
