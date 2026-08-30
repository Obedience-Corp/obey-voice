# B1e — qwen3:30b re-run after think-filtering: results

**2026-08-29/30 · Apple M4 Max, 128 GB, macOS 26.5, Ollama 0.32.14 · `num_batch: 128`**

- Gate, written first: [`B1-brain-latency-ab-criteria.md`](B1-brain-latency-ab-criteria.md)
- Raw artifact: [`B1e-brain-qwen30b-rerun-2026-08-30.json`](B1e-brain-qwen30b-rerun-2026-08-30.json)
- Prior run this re-measures: [`B1`](B1-brain-latency-ab-2026-08-28.md)
- Harness: [`scripts/brain_bench`](../../scripts/brain_bench) — `just bench brain`

## Why this run exists

`B1` disqualified `qwen3:30b` for a behavior, not a latency: it wrote raw
`<think>` blocks into message *content*, and the pipeline would have spoken
them. PRs #64 and #68 closed that at the pipeline seam — think regions are
filtered out of TTS and the transcript, and the TUI paints them as a dim
`thinking` aside. The disqualification was therefore lifted **pending a
re-measurement**, and this is it.

The incumbent `qwen3.8-uncensored:27b` is re-measured alongside it, because the
comparison would otherwise be unfair in the incumbent's disfavour: `B1` ran
before #67 made `ollama_num_batch: 128` the shipped default, and that setting is
worth several seconds to this exact model family. Both rows here are at the
same shipped defaults, on the same machine, in one serialized run.

## Headline

**`qwen3:30b`'s 176 ms first token was never a wait anyone experienced.** It is
the time to its first *`<think>`* token. With think content filtered out of
speech — which is now what ships — the first word a listener actually hears
arrives at **4.1 s** on a fresh conversation and **7.0 s** with 6k of history.

The incumbent, re-measured at the shipped `num_batch`, speaks first in
**2.2 s / 2.3 s**. On the number the listener experiences, **the incumbent is
1.8× faster empty and 3.0× faster at 6k** than the model whose raw TTFT is 25×
better.

Think-filtering **held**: across all 60 measured turns, zero think tags reached
the spoken stream.

**No default is changed by this PR, and none is recommended.** Model quality is
Lance's judgment; this is the latency half only.

## Results

Warm, `n = 15` per model per condition, one discarded warm-up per condition.
`ttft` stops on the first streamed token; `speak` stops on the first segment
that survives the pipeline's own spoken path (`ChunkSentencesRaw` → one
per-turn `textclean.ThinkFilter` → `CleanForVoice`) — i.e. the first text handed
to TTS.

| model | cond | ttft p50 | **speak p50** | speak worst | tok/s | worst turn | gate |
|---|---|---:|---:|---:|---:|---:|:--:|
| **qwen3:30b** | empty | **167 ms** | **4097 ms** | 25.8 s | 110.9 | 25.8 s | FAIL\* |
| | 6k | **202 ms** | **7024 ms** | 13.1 s | 90.2 | 13.4 s | pass |
| **qwen3.8-uncensored:27b** *(incumbent)* | empty | 925 ms | **2244 ms** | 8.7 s | 20.2 | 11.8 s | FAIL |
| | 6k | 894 ms | **2304 ms** | 21.2 s | 22.5 | 24.8 s | FAIL |

\* Two turns in that condition blew up, both in rep 3 and adjacent in time. One
(P2) took **21.4 s to its first token** with no tool call and a single model
request; at n=15 the p95 is that turn, so G2 fails on it. The other (P1) fired
`web_search` on "what's the capital of Australia?" — its first token arrived
normally at 153 ms, but the tool round trip pushed the turn to 25.2 s. The
remaining 13 turns are 152–222 ms to first token.

Zero errors and zero turn timeouts in 60 measured samples.

### The reasoning is the wait

The gap between the two TTFT columns is the reasoning prologue, and its size is
the whole story:

| model | cond | median reply generated | median reply **spoken** | discarded |
|---|---|---:|---:|---:|
| qwen3:30b | empty | 1761 chars | 162 chars | **91 %** |
| qwen3:30b | 6k | 2984 chars | 132 chars | **96 %** |
| qwen3.8-uncensored:27b | empty | 221 chars | 220 chars | 0 % |
| qwen3.8-uncensored:27b | 6k | 337 chars | 335 chars | 0 % |

`qwen3:30b` writes about 3,000 characters in order to say 132. Its 90–111 tok/s
generation rate — genuinely the fastest in the field — is spent almost entirely
on text that is deleted before anyone hears it. That is why a model 5× faster at
generating tokens is 3× slower to say its first word.

`think: "false"` does not prevent this. It sets Ollama's `think` field, which
suppresses the separate `thinking` field; it cannot stop a model that writes
`<think>` into `content`. The filter downstream removes it from speech, and the
tokens are still generated and still paid for.

## Did think-filtering hold?

**Yes, for tagged reasoning.** Every sample records the spoken text the pipeline
would have synthesized. `think_leaks` is **0** in all four conditions —
`<think>` never survives into speech, and the raw replies confirm the model
still emits the blocks, so the filter was exercised rather than merely unneeded.

**One thing the filter cannot reach.** On the tool-detour turn, `qwen3:30b`
narrated its reasoning as *untagged* content after `</think>`, and that is
spoken verbatim:

> "Okay, the user asked, 'What's the capital of Australia?' I used the
> web_search tool to find the answer. The search results from Wikipedia and
> Britannica all mention Canberra as the capital. Let me check the snippets
> again…"

Nothing in a think filter can catch that — there is no tag to match. It is a
model-behavior observation, not a regression in #64/#68.

## The incumbent's 4.6 s floor is gone

`B1` recorded a fixed ~4.6 s first-token cost for the qwen3.8 family, near
enough independent of prompt size, and built its FAIL verdict on it. At the
shipped `num_batch: 128` that cost is largely gone:

| model | cond | B1 ttft p50 | B1e ttft p50 | change |
|---|---|---:|---:|---:|
| qwen3.8-uncensored:27b | empty | 4637 ms | **925 ms** | −80 % |
| qwen3.8-uncensored:27b | 6k | 4733 ms | **894 ms** | −81 % |

That is the mechanism `B2` named and #67 shipped, now confirmed on the
conversational seam rather than against the raw API. The incumbent still fails
the B1 gate, but for a different reason than it did in `B1`: **G3**, generation
throughput of 20.2 / 22.5 tok/s against a 25 tok/s threshold, plus a G2 tail
(below).

Read the B1 column as a different run on a different day, not a controlled
before/after: only `num_batch` was the intended change, but the machine's other
conditions were not held.

## The tail, and what was running on the machine

Both models drew large excursions this run:

| model | cond | excursions | rest of the distribution |
|---|---|---|---|
| qwen3:30b | empty | 21.4 s ×1 (no tool call), plus a 25.2 s `web_search` turn | 152–222 ms |
| qwen3.8-uncensored:27b | empty | 8.0 s ×1 | 870–1561 ms |
| qwen3.8-uncensored:27b | 6k | 19.9 s, 20.0 s | 875–1186 ms |

**The machine was not idle.** A live `obey-voice` session was observed running
from 18:27 to ~18:32 during this run (an interactive process plus its own
`qwen3-tts-worker`), alongside a long-running VM. The excursions are not
attributable to the models on this evidence, and neither are they excused: they
are reported as observed and the medians are the figures to read. Re-running on
a quiet machine is the way to settle the tail; this run does not.

## Unprompted `run_command` — worth Lance's attention

On `filled_history_6k` P1 — the prompt is *"What's the capital of Australia?"* —
the **incumbent** produced this chain:

```
read_skill(camp-navigation) | run_command(cgo p)
| run_command(eval "$(camp shell-init zsh)" && cgo p)
| run_command(zsh -c 'eval "$(camp shell-init zsh)" && cgo p')
| run_command(ls -1 /Users/lancerogers/Dev/AI/My_Tools/projects/)
```

Four shell executions on a live workstation from a geography question, and it
then read the project list aloud. `B1` recorded this class of behavior for
`llama3.2` and treated it as a weak-model failure; here it is the model Lance
actually runs. The turn cost 24.8 s. This is the exposure WI-30709e is about,
and it belongs to `voice_tools_enabled`, not to the model.

## Cold load — not comparable this run

`qwen3:30b` 3.0 s, `qwen3.8-uncensored:27b` 17.3 s. Both are unusable as
figures: 30b's weights were already in the page cache from a preceding smoke
run, and the incumbent loaded while the machine was busy. `B1`'s cold table
(23.1 s and 9.2 s respectively) was measured under better-controlled conditions.

## Method

```bash
go run ./scripts/brain_bench \
  -models "qwen3:30b,qwen3.8-uncensored:27b" \
  -reps 3 -stop-between=true -id B1e \
  -out docs/benchmarks/B1e-brain-qwen30b-rerun-2026-08-30.json
```

- **Flush-cold by explicit unload**, not by nonce: every candidate is released
  with `keep_alive: 0` before it is measured (`-stop-between`), so the cold
  figure is a real load. `B2` established that a head nonce does not reliably
  defeat this server's checkpoint restore, which is why no nonce is used here.
  Unload, never delete — no model was pulled or removed.
- **Conversational rows are warm and repeated-prefix**, which is the state a
  real conversation is in: one discarded warm-up per condition puts the server's
  KV prefix cache in the state the second turn of any conversation finds it,
  and history is reset to the condition baseline before every sample so prompt
  size is constant across reps.
- **Shipped defaults**: `num_ctx 8192`, `num_batch 128` (#67), `think "false"`,
  `keep_alive 10m`, tools enabled, `obey` persona, 941-token system prompt.
- `~/.obey` was read, never written. The incumbent was re-warmed at the end.
- Statistics as in the criteria: at n=15 the "p95" is the worst or second-worst
  observation and is not a distributional claim. Medians are the figures to
  trust, and the tail here is unmeasured.

## What this does not say

It does not say which model is better to talk to. It does not recommend a
default. `qwen3:30b` is the fastest generator measured on this machine and its
replies are clean once filtered; whether ~4–7 s of silent reasoning before every
answer is acceptable is a product judgment, and it is Lance's.
