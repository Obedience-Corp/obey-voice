# B2 — qwen3.8's fixed TTFT: the mechanism, and the fix

**WI-adf58f · 2026-08-28/29 · Apple M4 Max, 128 GB, macOS 26.5, Ollama 0.32.14**

- Gate, written first: [`B2-qwen-ttft-mechanism-criteria.md`](B2-qwen-ttft-mechanism-criteria.md)
- Raw artifact: [`B2-qwen-ttft-mechanism-2026-08-28.json`](B2-qwen-ttft-mechanism-2026-08-28.json)
- Harness: [`scripts/ttft_probe`](../../scripts/ttft_probe) — `just bench ttft`
- Predecessor: B1 (PR #62, branch `bench/brain-latency-ab`), which measured the
  cost and left the cause open.

> **This document was corrected after review.** Its first version named the
> mechanism *prefill throughput* and was wrong: the sweep it argued from varied
> only the tail of the prompt, so it measured how much of the persona the cache
> could reuse and reported it as reading speed. The number on the box was right;
> the reason was backwards, and the follow-ups it recommended pointed the wrong
> way. What changed is recorded in [Correction](#correction) and in the
> artifact's `corrections` block. The criteria doc is **not** edited to match —
> retrofitting it would destroy the only record of what was predicted in advance.

## Headline

Ollama picks how many prompt tokens to read per pass from the **context window
alone** (`server/sched.go`, `generationBatchForContext`: 512 at or below
`num_ctx` 4096, 1024 above), never consulting the model. llama.cpp keeps its
last reusable **context checkpoint about one micro-batch back from the end of
the prompt**. Put together: at `num_ctx` 8192 the batch is 1024, a ~1.2k-token
conversational prompt has no usable checkpoint, and every turn re-reads the
persona, the tools and the history from scratch.

`qwen3.8-uncensored:27b` re-reads at ~240 tok/s, roughly **six times slower than
`gemma4:26b`** at the same job, so the same lost reuse costs it 4.3 s where it
costs gemma4 0.65 s. **The architecture explains the severity; the batch size
explains the fix.**

Sending `num_batch: 128` moves the checkpoint to ~128 tokens from the end. The
server then skips **89 %** of the prompt instead of 14 %, and the median first
token on the real product path goes **4472 ms → 864 ms**.

**Lance keeps `qwen3.8-uncensored:27b`.** Implemented as `ollama_num_batch`,
default 128.

It is a trade, not a free win, and the trade has a sign: **cold prompts get
slower**. That is [below](#the-trade-and-its-sign), with the p95 column intact.

## Correction

The first version reported this sweep as throughput:

| `num_batch` | prefill | "tok/s" |
|---:|---:|---:|
| 1024 | 4281 ms | 279 |
| 128 | 703 ms | 1698 |

Both prefill numbers are real. The rate column was `prompt_eval_count ÷
prompt_eval_duration`, and **`prompt_eval_count` counts tokens the server
skipped as well as tokens it read** — the artifact's own note said so, and the
note was applied to the cache-hit rows but not to the sweep the default was
chosen from. Left unchecked it produced `59,494 tok/s` on a gemma4 row. A model
does not read a six-times-longer prompt six times faster per token.

Every case now carries a `reuse` block parsed from llama.cpp's own slot log,
where `prompt eval time = 702.78 ms / 128 tokens` counts **only what was
actually read**. That is a rate that may be compared between cases. The renamed
`apparent_prefill_tok_per_s` may not, and is kept only so old numbers remain
traceable.

## The rule

`cached_tokens ≈ prompt_tokens − num_batch`, because llama.cpp creates a
checkpoint `4 + n_ubatch` tokens before the end of the prompt
([PR #20288](https://github.com/ggml-org/llama.cpp/pull/20288)). Measured
against predicted, on `qwen3.8-uncensored:27b` at ~1196 prompt tokens:

| `num_batch` | predicted cached | measured cached | measured share |
|---:|---:|---:|---:|
| 1024 | 172 | 167 | 14 % |
| 512 | 684 | 682 | 57 % |
| 256 | 940 | 945 | 79 % |
| 128 | 1068 | 1064 | 89 % |
| 64 | 1132 | 1136 | 95 % |
| 32 | 1164 | 1160 | 97 % |

Six for six. This is llama.cpp's documented checkpoint behaviour, not a local
quirk — see [Upstream](#upstream).

## Two sweeps, and they point opposite ways

The instrument is a nonce whose *placement* is the variable: in the user's last
sentence (`tail`, what a real conversational turn changes) or in front of the
system prompt (`head`, nothing reusable). Running only the first is the mistake
the review caught.

**Production pattern — persona and tools fixed, last sentence changes.** This is
what a voice turn does:

| `num_batch` | qwen3.8 prefill | cached | gemma4 prefill | cached | qwen3:8b prefill | cached |
|---:|---:|---:|---:|---:|---:|---:|
| 1024 | 4280 ms | 14 % | 650 ms | 0 % | 62 ms | 98 % |
| 512 | 2233 ms | 57 % | 421 ms | 45 % | 62 ms | 98 % |
| 256 | 1212 ms | 79 % | 274 ms | 72 % | 62 ms | 98 % |
| **128** | **703 ms** | **89 %** | **200 ms** | **86 %** | **62 ms** | **98 %** |
| 64 | 445 ms | 95 % | 156 ms | 93 % | 61 ms | 98 % |
| 32 | 339 ms | 97 % | 140 ms | 96 % | 62 ms | 98 % |

**True reading speed — llama.cpp's own count of tokens it actually read:**

| `num_batch` | qwen3.8 | gemma4:26b | qwen3:8b |
|---:|---:|---:|---:|
| 1024 | **239 tok/s** | **1432 tok/s** | **875 tok/s** |
| 512 | 228 | 1260 | 870 |
| 256 | 231 | 1074 | 854 |
| 128 | 220 | 839 | 788 |
| 64 | 205 | 591 | 676 |
| 32 | 155 | 385 | 560 |

Reading speed **falls** monotonically as the batch shrinks, on every model, in
both sweeps. Nothing here reads the prompt faster. The wall-clock win is
entirely the reuse column.

`qwen3:8b` is the control that proves the point: plain causal attention, ordinary
prefix reuse, **98 % cached at every batch size** and a flat 62 ms. It does not
depend on checkpoints, so the batch does not move it.

## The trade, and its sign

Cold prompts. Getting a genuinely cold read on the incumbent needed the cache
*emptied* — the model is unloaded before each sample — because a differing
prefix alone did not do it (see [the one thing not
explained](#what-this-does-not-fix-and-one-thing-it-does-not-explain)). Same
prompt size, same harness, `n = 4`:

| `num_batch` | qwen3.8 forced-cold prefill |
|---:|---:|
| 1024 | **5486 ms** |
| 512 | 5502 ms |
| 256 | 5621 ms |
| **128** | **5893 ms** |

**+407 ms at 128**, and monotonic. The reviewer's independent spot-check put the
same trade at +358 ms. For the two models whose head-nonce rows *did* verify at
0 % cached, the same sign is much larger:

| `num_batch` | gemma4:26b cold | qwen3:8b cold |
|---:|---:|---:|
| 1024 | **722 ms** | **1146 ms** |
| 128 | 1228 ms | 1275 ms |
| 32 | 2672 ms | 1794 ms |

gemma4 pays **+70 %** at 128, qwen3:8b **+11 %**.

This lands exactly where it hurts, and the product-path table shows it:

| condition | TTFT p50 | **TTFT p95** | gen tok/s | turns < 2 s |
|---|---:|---:|---:|---:|
| empty history — before | 4472 ms | **4663 ms** | 24.2 | 0 / 10 |
| empty history — after | **864 ms** | **7559 ms** | 24.1 | 14 / 15 |
| 6k history — before | 4844 ms | **24815 ms** | 23.2 | 0 / 10 |
| 6k history — after | **948 ms** | **19744 ms** | 23.1 | 13 / 15 |

Per-turn rows are in the artifact's `product_path` block so this is
re-derivable here rather than only on B1's unmerged branch.

**The case for taking it anyway**, said out loud rather than by omitting the
column: a voice assistant's turns are overwhelmingly the production pattern —
the persona, the tool schemas and the history are fixed, and the user adds one
sentence. Before, *every* one of those turns cost 4.4 s and the user heard a dead
pause on all of them. After, 27 of 30 cost under a second and three cold reads
cost 7.6 s and 19.7 s. Trading a uniform 4.4 s wall for a 0.9 s median with an
occasional long turn is worth it for conversation, and it would not be worth it
for a batch job that sends a fresh prompt every time — which is why this is a
setting and not a constant, and why `0` still hands the decision back to Ollama.

Generation is unaffected: 24.1 tok/s against 24.2, and the probe's own
`eval_count / eval_duration` is flat at 23 tok/s across the sweep. An earlier
single-rep run appeared to show a 25 % generation regression; that was
`brain_bench` deriving tok/s as `gen_tokens / (total − ttft)`, where collapsing
TTFT inflates the denominator.

## Why 128

| model | production path @1024 | @128 | won | cold @1024 | cold @128 | cost |
|---|---:|---:|---:|---:|---:|---:|
| qwen3.8-uncensored:27b | 4403 ms | **827 ms** | −3576 ms | 5486 ms | 5893 ms | +407 ms |
| gemma4:26b | 844 ms | **393 ms** | −451 ms | 722 ms | 1228 ms | +506 ms |
| qwen3:8b | 140 ms | 138 ms | −2 ms | 1146 ms | 1275 ms | +129 ms |

Each row is one measurement source at one prompt size, so the two halves are
comparable: the incumbent's cold cells are the forced-cold run above, the other
two models' are their verified-0 %-cached rows.

The incumbent trades **+407 ms on a cold turn for −3576 ms on a conversational
one**. gemma4 trades +506 ms for −451 ms, which is roughly a wash and would not
justify the change on its own; qwen3:8b is indifferent on the production path
and pays 129 ms cold. The change is carried by the model it was chosen for, and
that is worth saying plainly rather than claiming it helps everything.

64 and 32 buy little more on the production path and cost a great deal cold —
the incumbent reaches 8.4 s at batch 32 with a head nonce — so the sweep does
**not** recommend pushing lower.

## Upstream

- **[llama.cpp PR #16382](https://github.com/ggml-org/llama.cpp/pull/16382)**
  (merged) implements context checkpointing for hybrid and recurrent models —
  the mechanism everything here depends on.
- **[llama.cpp PR #20288](https://github.com/ggml-org/llama.cpp/pull/20288)**
  (merged) is where the measured rule comes from. It creates checkpoints at two
  fixed offsets before the end of the prompt, and the diff names them:

  ```c
  // create checkpoints that many tokens before the end of the prompt:
  static const int checkpoint_offsets[] = {4 + n_ubatch, 4};
  ```

  `4 + n_ubatch` before the end is exactly the `cached ≈ prompt − num_batch`
  measured above, to within the few tokens the offset adds.
- **[llama.cpp PR #25472](https://github.com/ggml-org/llama.cpp/pull/25472)**
  (merged) evicts checkpoints that fall within `checkpoint_min_step` of each
  other, which can remove the near-end one. Relevant, but it is *not* where the
  `4 + n_ubatch` constant comes from — that is #20288 above.
- **[llama.cpp #24055](https://github.com/ggml-org/llama.cpp/issues/24055)**
  (open) is the live hybrid/recurrent checkpoint-invalidation case; its fix
  [#25592](https://github.com/ggml-org/llama.cpp/pull/25592) is still open.
- **[llama.cpp #18725](https://github.com/ggml-org/llama.cpp/issues/18725)** is
  the one genuine prompt-throughput-versus-ubatch report on this family —
  Qwen3-**Next** on **Vulkan**/Strix Halo, root-caused to the `SSM_CONV` shader
  and **fixed** by [#20379](https://github.com/ggml-org/llama.cpp/pull/20379).
  Cited for direction only; it is not this machine's backend.

Two citations from the first version are **withdrawn**:
[#17578](https://github.com/ggml-org/llama.cpp/issues/17578) is an allocator
crash at `--ubatch-size > 512`, not a throughput report (fixed by #17794); and
[#20225](https://github.com/ggml-org/llama.cpp/issues/20225) was cited as open
when it was closed as not-a-bug hours after filing.

## What this does not fix, and one thing it does not explain

**The cold tail.** Three turns in thirty still lose the cache and pay a full
re-read, now slightly more expensive than before. The upstream fix for the
hybrid/recurrent case is unmerged (#25592).

**An observation, not a diagnosis.** On `qwen3.8-uncensored:27b` *only*, a
request whose entire 90-word leading prefix differs still restores a checkpoint
about one micro-batch from the end — 21 % reported cached at batch 1024, 61 % at
512 — instead of reading the prompt cold. Moving the varying text to the very
first token did not change it. `gemma4:26b` and `qwen3:8b` go to 0–1 % under
identical construction.

The forced-cold run above measures what it costs: with the cache actually
emptied the incumbent needs **5486 ms** at batch 1024, against **4284 ms** for
the same prompt with a fully different prefix and no flush. So the restore is
real and it is saving work on a prefix that does not match. Replies were correct
in every sample. Recorded, not explained — and it is why the cold numbers in
this document come from the forced-cold run rather than from a nonce.

**One machine, one Ollama build, three models.** The checkpoint rule is
upstream behaviour and should reproduce; 128 is tuned against an M4 Max, which
is why it is a setting.

**Reply quality is not measured.** Nothing here says the incumbent is better or
worse than `gemma4:26b` at being Lance's assistant.

## What changed in the code

- `ollama_num_batch` (default 128), sent on every Ollama chat call. `0` restores
  Ollama's own choice.
- `Warmup` builds its options from `requestOptions()` instead of assembling a
  second map by hand — Ollama reloads the model whenever a request's options
  differ from the loaded runner's, so drift between the two buys a model load
  the user's first sentence pays for. The one deliberate difference
  (`ollama_num_ctx: 0`) is pinned by a test rather than left to be discovered.
- The batch provider (`batch.go`) now sends the same options, `keep_alive` and
  `think`. It previously sent none of them: it would evict the warm
  conversational runner on every transform, and run with reasoning **on**.
