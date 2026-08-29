# B1 — conversational brain model A/B: results

**WI-a981e4 · 2026-08-28 · Apple M4 Max, 128 GB, macOS 26.5, Ollama 0.32.14**

- Gate, written first: [`B1-brain-latency-ab-criteria.md`](B1-brain-latency-ab-criteria.md)
- Raw artifact: [`B1-brain-latency-ab-2026-08-28.json`](B1-brain-latency-ab-2026-08-28.json)
- Supporting runs: [`B1b`](B1b-brain-tool-side-effects-2026-08-28.json) (tool
  side effects) · [`B1c`](B1c-brain-tools-off-2026-08-28.json) (tools-off floor)
  · [`B1d`](B1d-brain-gemma4-rerun-2026-08-29.json) (gemma4 re-run, tail)
- Harness: [`scripts/brain_bench`](../../scripts/brain_bench) — `just bench brain`

## Headline

The incumbent fails, and **not because it is large**. `gemma4:26b` is the same
size class — 18.0 GB against the incumbent's 17.7 — and answers **12× sooner**.
The dead pause Lance hears is a fixed per-turn cost specific to the qwen3.8
family on this machine. It is nearly independent of how much context sits in
front of it, so shrinking the conversation would not have fixed it either.

**Recommendation: make `gemma4:26b` the `obey` persona's conversational default.**

> **Decision (2026-08-28, Lance):** declined — the quality judgment the criteria
> reserved to him: qwen3.8-uncensored is better on this machine, and product
> defaults must never pin a large model (the obey persona correctly pins
> nothing). The measurement stands as record; the follow-up is WI-adf58f,
> finding the incumbent's fixed-TTFT mechanism so quality and speed stop being
> a trade.
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
| | 6k | **388 ms** | **423 ms**† | 92.5 | **1.3 s** | **pass** |
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

Every candidate's TTFT is close to flat across a ~3.5–4× change in prompt size —
the server's KV prefix cache is doing its job. What separates the models is a
**fixed per-turn cost**.

Prompt sizes below are each model's own real `prompt_eval_count`, not a shared
label: tokenizers differ, so there was never one "1.4k".

| model | disk | empty: TTFT @ real prompt | 6k: TTFT @ real prompt | prompt ratio | growth |
|---|---:|---:|---:|---:|---:|
| qwen3.8-uncensored:27b | 17.7 GB | 4637 ms @ 1656 tok | 4733 ms @ 5835 tok | 3.5× | +2 % |
| qwen3.8:latest | 17.7 GB | 4639 ms @ 1656 tok | 4897 ms @ 5963 tok | 3.6× | +6 % |
| gemma4:26b | 18.0 GB | 364 ms @ 1398 tok | 388 ms @ 5553 tok | 4.0× | +7 % |
| qwen3:8b | 5.2 GB | 170 ms @ 1382 tok | 199 ms @ 5561 tok | 4.0× | +17 % |

Two models of essentially identical size, one 12× slower to speak, and the gap
does not come from context pressure. **The pre-registered hypothesis that
`num_ctx` was half of Lance's pain is wrong**: a 3.5× larger prompt costs the
incumbent between **+96 ms (+2 %) and +420 ms (+9 %)** — this run measured the
former, an independent n=5 re-run on a different day measured the latter. Treat
the range as the finding, not either endpoint; against a 4.7 s wait, both are
noise next to the fixed cost.

A diagnostic run isolates how little of it is ours to fix. With tools disabled —
committed as [`B1c-brain-tools-off-2026-08-28.json`](B1c-brain-tools-off-2026-08-28.json),
same harness at `6f2e4fb` — the prompt drops from 1656 to **840** real tokens
(the schemas) and TTFT p50 falls from 4637 ms to **3812 ms**. So removing 816
tokens buys back 825 ms and leaves a **~3.8 s floor** on an 840-token prompt.
Nothing in the config surface — tools, context window, think — removes that
floor.

What causes it is **not established by this benchmark**. It is a property of the
qwen3.8 family under Ollama 0.32.14 on this machine. The effect is large and it
reproduces — an independent n=5 re-run measured p50 4676 ms empty / 5096 ms at
6k against this run's 4637 / 4733, and 23.9 tok/s on the nose.

The *floor* is consistent; the **tail is not**. This run's spread looks tight
(p50 4637 ms, p95 4824 ms) but that p95 is just its maximum at n=15, and the
re-run drew a 22.5 s first token at 6k. So: reliably ~4.6 s at best, with
occasional multi-second excursions on top. That makes the FAIL verdict stronger,
not weaker, and it is why the tight-looking spread should not be quoted as
evidence of consistency.

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

**`llama3.2:latest` called a tool on 28 of its 30 measured turns** (`clean_turns`
0/15 empty, 2/15 filled), including `web_search` for "what's the capital of
Australia?". Every one is an extra model round trip, and the `web_search` ones
are an outbound HTTP request in the middle of a conversation.

**`dolphin-mistral`** stays clean and fast but answers in numbered lists
(`Here's an order you could follow:1. Review the pull request:…`), which is
markdown structure being read aloud.

### Unprompted side effects — worth Lance's attention on its own

Tool calls in **this** run (`B1…json`), by model:

| model | calls |
|---|---|
| qwen3:30b | `web_search` ×4 |
| llama3.2:latest | `read_skill` ×21, `web_search` ×6, `run_command` ×2, `read_file` ×1 |
| llama3.3:latest | `web_search` ×15, `read_skill` ×6 |
| gemma4:26b, qwen3:8b, kimi, dolphin-mistral, both qwen3.8 | none |

**The two `run_command` calls in this run created nothing.** They are
`run_command(web_search --query "voice model comparison")` and
`run_command(read_skill list_files)` — llama3.2 invoking the *names of its own
tools* as if they were shell binaries. Malformed, harmless, and not evidence of
anything except that the model is confused.

**The file-writing incident is real but happened in a different run**, and an
earlier version of this document sourced it wrongly. Correcting it:

It was a pre-registration smoke run — `-models llama3.2:latest -reps 2`,
`generated_at 15:22:17`, harness uncommitted at that point (the tree was at
`0480fd0`, the criteria commit). It is now committed as
[`B1b-brain-tool-side-effects-2026-08-28.json`](B1b-brain-tool-side-effects-2026-08-28.json)
so the claim is checkable. On **measured sample P4, rep 1** — not a discarded
warm-up — llama3.2 emitted this chain from a prompt that merely *mentions*
replying to email and reviewing a pull request:

```
read_skill(camp-structure) | list_files(Slides/Slide.pptx) | read_file(Slides/Slide.pptx)
| run_command(open Slides/Slide.pptx) | read_skill(campaign-commit)
| write_file(Email/Reply.txt) | list_files(Email/Reply.txt) | run_command(open Email/Reply.txt)
```

That `write_file` created `Email/Reply.txt` containing *"My feedback on this pull
request."*, and those two `run_command(open …)` calls **executed `open` on a live
workstation**. A later sample in the same run ran
`run_command(wiki --http --format json https://en.wikipedia.org/w/api.php…)`.

`Hello.txt` (containing `Hello`) appeared at 15:48, during the committed run, and
**no record of it survives** — the only plausible source is a discarded warm-up
turn (`W0` is literally *"Say hello in one short sentence."*), whose tool names
are captured and then dropped with the sample. That is an inference, not a
finding; warm-up side effects are invisible by construction, which is itself
worth fixing in the harness.

Both files were deleted after the run.

The blast-radius conclusion is unchanged and, if anything, stronger now that the
shell execution is sourced: with `voice_tools_enabled: true`, a weak model will
write files and run commands in the agent's working directory because a sentence
sounded vaguely like a request. The four recommended-tier models did none of it —
but the exposure belongs to the configuration, not to whichever model happens to
be selected. Worth a separate look at whether `write_file` and `run_command`
should require something stronger than a model's unassisted judgment on a voice
turn.

**Caveat on B1b's provenance.** It was produced by an uncommitted harness state,
so it is evidence rather than a reproducible artifact: the warm-up still reused
P1 (fixed in `6f2e4fb`), which is why its own latency numbers are not cited
anywhere and should not be. Its tool-name records are unaffected by that bug. It
was deliberately **not** re-run against the committed harness: reproducing it
means provoking arbitrary `run_command` execution on Lance's machine again, which
is not a thing to do for tidiness.

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

The strongest result in the field on the metric that matters most for a voice
agent — a **median first token of ~0.39 s at full context**, holding across three
separate runs.

- TTFT p50 388 ms at 6k context, and **the median is the most reproducible number
  in this whole benchmark**: 388 ms here, 447 ms in an independent n=5 re-run,
  386 ms in a second one. The listener's normal experience is a third of a
  second.
- († in the results table) Its tail is *not* proven tight, and this run's 423 ms p95 should not be quoted
  as if it were. A later n=5 re-run
  ([`B1d`](B1d-brain-gemma4-rerun-2026-08-29.json)) drew a single 7.9 s first
  token out of 5 samples at 6k — enough to fail G2 on that run. On that same
  sample the unrelated skill-router embedding call also stalled 20× (345 ms
  against a 17 ms norm), which points at a whole-server hiccup rather than
  anything about gemma4; but that is an inference, and one excursion in 25
  observed 6k samples is the honest characterization. The p50 is the claim; the
  tail is a machine property this benchmark cannot pin down.
- 92.5 tok/s — 4× the incumbent.
- Replies run 88–426 characters (medians 216 empty / 249 at 6k): the 2–3
  speakable sentences the turn instruction asks for, without being clipped.
- Clean on every sampled turn — no leaked reasoning, no unprompted tools, no
  markdown lists.
- **18.0 GB, against the incumbent's 17.7 GB.** No memory regression, and a 26B
  model rather than a downgrade to something small. It is already pinned by the
  `dylan` persona, so it is not an unknown quantity.

**Confidence: high** on the central latency claim — a 12× margin on a median that
reproduced three times, in both conditions. That margin is far too large for the
one tail excursion above to threaten it: even that 7.9 s outlier is inside the
band the incumbent sits in *typically*.

**Confidence: moderate** on it being the right *overall* answer, because that
depends on whether Lance likes talking to it, which this benchmark cannot tell
him.

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

### 3. `num_ctx` is the wrong key to look at — `max_history` binds first

**This section replaces an earlier version that was wrong, and the correction
matters more than the original point.**

The criteria doc justified measuring at 6k with: *"`ollama_num_ctx` is 8192. A
real conversation fills that."* **It does not, and cannot.** `max_history: 10`
plus `trimHistory` (`ollama.go:754`) hold retained history to **≤ 20 messages**:
nothing is dropped until the count exceeds `MaxHistory*2`, then it cuts back to
`MaxHistory`. Measured against the same estimator the harness uses, a 20-message
live conversation tops out at **~2,590 estimated tokens** — under a third of
`num_ctx`.

Two consequences, both reframing rather than reversing the conclusion:

**The 6k condition is a session-resume edge, not the live path.** It is still
reachable: `LoadHistory(sess.Turns)` on resume (`root.go:250`, `root.go:442`,
`serve.go:322`) can seed far more than 20 messages, and `trimHistory` does not
fire until the end of that first turn — so the first turn after resuming a long
session really does carry a prompt this size. As a stress condition it errs
toward the conclusion, since the incumbent fails at *both* sizes. But it should
not be read as "a normal conversation", and the criteria doc's rationale for it
was mistaken.

**Raising `num_ctx` buys no conversational memory while `max_history` is 10.**
The binding constraint is the message cap, not the token window. Anyone tuning
for a longer memory has to move `max_history` first; `num_ctx` only has to be
large enough not to clip it, and at 8192 it already is — roughly 3× over.
`ensureContextBudget`'s 90 % trim line (7,372 tokens) is likewise unreachable in
live chat.

**Leave both alone in this change.** With a fast default there is headroom to
raise `max_history` cheaply — gemma4 pays 24–59 ms going from ~1.4k to ~5.6k real
prompt tokens — but that is a follow-up to measure, not something to change blind
alongside a model swap.

The layering complaint survives and gets sharper: **`ollama_num_ctx` *and*
`max_history` are both app-level keys while the model is per-persona.**
`internal/persona/overrides.go` routes `ollama_model`, `brain_provider` and the
TTS keys — neither of the two that govern how much conversation the model reads.
So switching to a persona pinning a different-sized model changes the model and
leaves its context budget behind. The right unit for that tradeoff is the
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

**n=15 per model per condition.** At n=15 the nearest-rank p95 resolves to rank
15 — it *is* the maximum, in all 18 rows. Read the p95 column as **worst
observed**; it is not a distributional claim, and a tighter p50/p95 spread means
only that this run never drew a tail, not that no tail exists. An independent
n=5 re-run drew a 22.5 s first token from the incumbent at 6k where this run's
worst was 5.5 s. The medians are the numbers to trust. (`bench_test.go` pins the
p95≡max identity so raising `-reps` cannot silently change what the column
means.)

**Live machine.** This ran on Lance's daily driver against his live Ollama
server. Contention is a real error term and no confidence interval here would be
honest. Runs were serialized, one model resident at a time.

That error term is not hypothetical: across three runs of gemma4 the medians
agree to within a few ms, while one n=5 re-run drew a 7.9 s first token whose
sample *also* showed a 20× stall in an unrelated embedding call. Medians here are
solid; individual maxima say as much about the machine that day as about the
model. This is the reason the gate is judged on both a median and a worst case,
and the reason a single re-run flipping a p95 does not overturn a p50 that
reproduces three times.

**Tools stay enabled**, because `voice_tools_enabled: true` is what Lance runs.
The gate counts tool detours, since the user waits through them.
`clean_turns_summary` in the JSON gives the tool-free view.

**Deviations from the pre-registered plan.**

1. The criteria said the filled condition would be sized so the *estimated*
   prompt lands near 6,000 tokens. The authored history tops out at ~5,934
   estimated / ~5,835 real tokens, so the condition is ~5.9k rather than exactly
   6k. The estimator and the server agree to within 2 %, and the conclusion does
   not turn on the difference.
2. **The criteria's rationale for that condition was wrong.** It said "a real
   conversation fills" `num_ctx`; `max_history: 10` means it cannot. See §3 — the
   condition is a session-resume edge, not the live path. The criteria doc is
   left as written, because the point of pre-registering it is that it records
   what was believed beforehand; the correction belongs here.
3. **One installed model was neither a candidate nor an exclusion.**
   `hf.co/lordx64/Qwen3.6-35B-A3B-Kimi-K2.6-Reasoning-Distilled-GGUF:IQ4_XS`
   carries a different digest from `kimi-k2.6-distilled:IQ4_XS` and was missed
   when the exclusion list was written. It is the same 35B-A3B distill family
   already represented by the measured kimi tag, so measuring it would not change
   the decision — but the criteria doc claimed a complete accounting of the
   install list and did not have one. Recorded rather than quietly fixed.

**`worktree_clean: false` in the artifact is not uncommitted code.** All harness
code was committed at `6f2e4fb` (15:25:33), six seconds before the run stamped
`generated_at 15:25:39`.

The flag's cause is `Email/Reply.txt`, written at 15:22:17 by the smoke run
described above and still untracked when the run started. An earlier version of
this document blamed "the artifact being written into `docs/benchmarks/`", which
is impossible: `provenance()` runs at `main.go:313`, *before* the model loop, so
nothing the run itself produces — neither the artifact nor any side effect — can
appear in its own provenance. `Hello.txt` cannot be the cause either; it did not
exist until 15:48.

**Machine left as found.** `~/.obey` was read, never written. Nothing was pulled
or deleted; models were unloaded (`keep_alive: 0`) to measure cold load, which is
a load and not a removal. The incumbent was re-warmed at the end and confirmed
resident.
