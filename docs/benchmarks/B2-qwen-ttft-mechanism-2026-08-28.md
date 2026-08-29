# B2 — qwen3.8's fixed TTFT: the mechanism, and the fix

**WI-adf58f · 2026-08-28/29 · Apple M4 Max, 128 GB, macOS 26.5, Ollama 0.32.14**

- Gate, written first: [`B2-qwen-ttft-mechanism-criteria.md`](B2-qwen-ttft-mechanism-criteria.md)
- Raw artifact: [`B2-qwen-ttft-mechanism-2026-08-28.json`](B2-qwen-ttft-mechanism-2026-08-28.json)
- Harness: [`scripts/ttft_probe`](../../scripts/ttft_probe) — `just bench ttft`
- Predecessor: B1 (PR #62, branch `bench/brain-latency-ab`), which measured the
  cost and left the cause open.

## Headline

The 4.3 s is **prompt reading**, and it is **not the model's fault**. Ollama
picks how much of the prompt to read per pass from the context window and
nothing else — 512 tokens per pass at or below `num_ctx` 4096, 1024 above it —
with no regard for the model's architecture. `qwen3.8` is a hybrid
attention/SSM model whose prompt-reading throughput *collapses* as that number
grows. Raising `num_ctx` to 8192 so the assistant could hold a conversation is
what doubled the batch to 1024, and that is what bought the dead pause.

Sending `num_batch: 128` on the same request, at the same `num_ctx` 8192, takes
the incumbent from **4472 ms to 864 ms median first token on the real product
path** — a 5.2× improvement — with **no cost to generation speed** and no
change to the model, the persona, the context window, or anything Lance chose.

**Lance keeps `qwen3.8-uncensored:27b`.** The fix is implemented in this PR as
`ollama_num_batch`, default 128.

It is not a total victory, and the honest part is at the bottom: roughly one
turn in ten still pays a full cold prompt read, so the p95 is still poor.

## The primary hypothesis was wrong

The criteria doc ranked hidden thinking first (M1/M2) and said the prior was a
prior. It is dead, and cleanly:

| condition | thinking chars | first *anything* | first *content* |
|---|---:|---:|---:|
| `think: false` (production) | **0** | 4423 ms | 4423 ms |
| `think: true` | 153 | 4452 ms | 5994 ms |
| `think` omitted | 196 | 4445 ms | 6681 ms |

With `think: false` the model emits **no reasoning tokens at all**, and nothing
arrives on *any* channel for 4.4 s — `first_any` and `first_content` are the
same number. Thinking cannot be hiding in a window where no tokens exist. The
flag also turns out to be load-bearing for a different reason: **omitting it
turns thinking on**, so `ollama_think: "false"` is doing real work and should
stay.

The `/no_think` fix candidate is dead with it — 4490 ms, indistinguishable from
doing nothing, which is what you would expect once thinking is already off.

## Where the time actually goes

Every millisecond is inside Ollama's own `prompt_eval_duration`. `load_duration`
is a flat ~140 ms on every warm sample, so it is not a reload, and generation
starts the instant prompt reading ends.

The decisive comparison is what the KV cache is allowed to reuse. Three cases,
identical except for where a unique nonce is placed:

| case | what changes between samples | prefill | tok/s |
|---|---|---:|---:|
| `cache-exact-repeat` | nothing — byte-identical | **146 ms** | — |
| `cache-tail-vary` | the user's last sentence only | 4279 ms | 279 |
| `cache-head-vary` | a nonce in front of the system prompt | 4296 ms | 278 |

Changing the last few tokens of the user's message costs **exactly as much** as
invalidating the entire prompt. There is no partial reuse: every conversational
turn is a full prompt re-read. And a conversational turn always changes the
prompt, which is why B1 saw a fixed cost that barely moved with context size.

`qwen3:8b` shows what working reuse looks like — `cache-tail-vary` 62 ms against
`cache-head-vary` 1038 ms, a 17× difference. `gemma4:26b`, though, shows
**651 ms against 649 ms**: it does not get partial reuse either. It is simply
fast enough that a full re-read costs 651 ms instead of 4.3 s.

So the lost reuse is real but it is *not* what separates the incumbent from
`gemma4`. Throughput is.

## Why the throughput is low, and the knob that fixes it

Two facts, one from the model file and one from Ollama's scheduler.

**The model.** `qwen3.8-uncensored:27b` reports `qwen35.full_attention_interval = 4`
alongside a full set of `qwen35.ssm.*` keys (`state_size`, `conv_kernel`,
`inner_size`, `group_count`). One layer in four is attention; the other 49 of
its 65 blocks are recurrent SSM layers. `gemma4:26b` has sliding-window
attention and no SSM keys at all; `qwen3:8b` is plain attention. The one
architectural feature the slow model has and the two fast ones do not is the
recurrent stack.

**The scheduler.** Ollama derives the prompt batch from the context window
alone (`server/sched.go`, `generationBatchForContext`): ≤ 4096 → 512, > 4096 →
1024, > 32768 → 2048. It never consults the model. That is why the window sweep
below has a step at exactly 4096 and is otherwise flat — the window was never
the variable, the batch was.

| `num_ctx` (prompt held at ~1195 tokens) | Ollama's batch | prefill | tok/s |
|---:|---:|---:|---:|
| 1024 | 512 | 2232 ms | 535 |
| 2048 | 512 | 2233 ms | 535 |
| 4096 | 512 | 2232 ms | 535 |
| 8192 | 1024 | 4279 ms | 279 |
| 16384 | 1024 | 4280 ms | 279 |

Setting `num_batch` explicitly disables that automatic choice. Held at the
production `num_ctx` of 8192:

| `num_batch` | prefill | tok/s | first content | vs. Ollama's own choice |
|---:|---:|---:|---:|---:|
| **1024** *(what Ollama picks)* | 4281 ms | 279 | 4403 ms | — |
| 512 | 2234 ms | 534 | 2356 ms | 1.9× |
| 256 | 1215 ms | 983 | 1336 ms | 3.3× |
| **128** | 703 ms | 1698 | **827 ms** | **5.3×** |
| 64 | 445 ms | 2681 | 566 ms | 7.8× |
| 32 | 339 ms | 3516 | 464 ms | 9.5× |

Monotonic across a 32× range, and it holds at conversation size — at ~7.5k
prompt tokens, batch 1024 gives 4703 ms and batch 64 gives 611 ms.

This matches an upstream defect, not a mystery: llama.cpp
[#18725](https://github.com/ggml-org/llama.cpp/issues/18725) and
[#17578](https://github.com/ggml-org/llama.cpp/issues/17578) both report
Qwen3-Next/3.5 prompt throughput falling sharply as `ubatch` grows, and
[#20225](https://github.com/ggml-org/llama.cpp/issues/20225) is the missing
prefix reuse on exactly this hybrid attention+SSM family, still open.

### It does not cost generation, and it helps two of three models

The obvious worry is that `num_batch` is passed to llama.cpp as both `-b` and
`-ub`, so a prefill win might be paid for during speech. Measured on
~270-token replies:

| `num_batch` | generation | first content | whole turn |
|---:|---:|---:|---:|
| 1024 | 19 tok/s | 4453 ms | 17292 ms |
| 512 | 23 tok/s | 2359 ms | 13687 ms |
| 256 | 23 tok/s | 1339 ms | 12460 ms |
| 128 | 23 tok/s | 821 ms | 9278 ms |
| 64 | 23 tok/s | 567 ms | 12015 ms |

Generation is 21 % **faster** at every reduced batch, not slower.

Across models, at ~930 prompt tokens:

| model | batch 1024 | batch 256 | batch 128 | batch 64 |
|---|---:|---:|---:|---:|
| qwen3.8-uncensored:27b | 4403 ms | 1336 ms | **827 ms** | 566 ms |
| gemma4:26b | 927 ms | 540 ms | 523 ms | 426 ms |
| qwen3:8b | 184 ms | 196 ms | 199 ms | 211 ms |

128 was chosen as the default because it is a large win for the incumbent, a
1.8× win for `gemma4:26b`, and costs `qwen3:8b` 15 ms. 64 is faster still for
the incumbent; it is available, not imposed, because a default set from three
models on one machine should not be the most extreme value that machine liked.

## Verified on the product path, not just the probe

The probe builds its own prompt. The number that matters is the one the brain
seam produces, so `brain_bench` from B1 was rebuilt against this branch's brain
package and run through a recording proxy that captured what actually went over
the wire (`{"num_batch":128,"num_ctx":8192}` on every chat).

Same harness, same persona, same machine, `n = 15` per condition:

| condition | TTFT p50 | TTFT p95 | gen tok/s | turns under 2 s |
|---|---:|---:|---:|---:|
| empty history — B1 (before) | 4472 ms | 4663 ms | 24.2 | **0 / 10** |
| empty history — with the fix | **864 ms** | 7559 ms | 24.1 | **14 / 15** |
| 6k history — B1 (before) | 4844 ms | 24815 ms | 23.2 | 0 / 10 |
| 6k history — with the fix | **948 ms** | 19744 ms | 23.1 | **13 / 15** |

Generation is unchanged (24.1 against 24.2), which also settles a false alarm
from an earlier single-rep run: `brain_bench` derives tok/s as
`gen_tokens / (total − ttft)`, so collapsing TTFT inflates that denominator and
made the fix look like a 25 % generation regression. Ollama's own
`eval_count / eval_duration` shows no such thing.

## What this does not fix, stated plainly

**The p95 is still bad.** Before, every turn cost 4.4 s. Now 27 of 30 turns come
in under 2 s and **three do not** — 7.6 s on a 1.6k prompt, 19.7 s on a 5.8k
one. Those are turns where the cache is lost entirely and the full prompt is
re-read at the old ~300 tok/s. The batch setting made the common path fast; it
did not give the model the partial prefix reuse it structurally cannot have
(llama.cpp #20225, open). A voice assistant that is usually 0.9 s and
occasionally 20 s is a real improvement over one that is always 4.5 s, and it is
not the same thing as fixed.

**B1's gate still fails**, on p95, and this document does not move the gate to
match the result.

**One machine, one Ollama build, three models.** The mechanism is architectural
and should reproduce anywhere, but the specific number 128 is tuned against an
M4 Max. The setting exists so it can be re-tuned rather than re-argued.

**Reply quality is not measured here.** Nothing in B2 says the incumbent is
better or worse than `gemma4:26b` at being Lance's assistant. It says the
latency reason for abandoning it has largely gone away.

## What changed in the code

- `ollama_num_batch` (default 128) on the Ollama brain, sent on every chat call.
  0 restores Ollama's own choice.
- `Warmup` sends the same options as real turns. This matters more than it
  looks: Ollama reloads the model whenever a request's options differ from the
  loaded runner's, so a warmup that omitted `num_batch` would warm a runner the
  user's first sentence immediately threw away — buying a model load in the
  middle of hello.

## Follow-ups worth their own look

1. **The residual p95.** Upstream is where partial reuse lives (llama.cpp
   #20225 / #24055). Worth watching, and worth checking again after an Ollama
   bump rather than being designed around.
2. **`num_batch` below 128 as a per-persona setting.** 64 nearly halves the
   incumbent's first token again; a depth persona pinned to qwen3.8 might want
   it while a fast persona on qwen3:8b does not.
3. **The window sweep is now misleading on its own.** Anyone reading
   `ollama_num_ctx` as a latency knob is reading a batch effect. It is not a
   latency knob any more, because the batch is no longer derived from it.
