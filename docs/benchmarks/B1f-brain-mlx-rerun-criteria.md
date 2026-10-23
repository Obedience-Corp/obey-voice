# B1f — brain-model rerun on Ollama 0.40.9 (MLX): criteria

**WI-524bb9. Written before the run.** Same instrument and gate as
[B1](B1-brain-latency-ab-criteria.md): `scripts/brain_bench` through the real
brain seam, `obey` persona, both history conditions, three warm reps,
`-stop-between` so cold loads are real.

## Why rerun

B1 (2026-08-28, Ollama 0.32.14) found the qwen3.8 family carrying a fixed
~4.6 s time-to-first-token on this machine while `gemma4:26b` answered in
364 ms. Lance declined gemma4 on quality; WI-adf58f tracks the qwen3.8
mechanism. Since then Ollama 0.40.0 added an MLX runner for supported
architectures on Apple Silicon, reachable through `-mlx` tagged builds, and the
llama.cpp runner moved several versions. The live brain today is
`dolphin-mistral` (Mistral 7B, 2023), not qwen3.8.

## Hypothesis

The qwen3.8 fixed TTFT is a llama.cpp-runner cost on this machine, so
`qwen3.8:27b-mlx` lands under the 1 s gate while `qwen3.8:latest` (llama.cpp)
does not. If both tags pass, the version bump fixed it; if neither does, the
cost is in the model or the prompt, not the runner.

## Candidates

| model | runner (observed 2026-10-23, `ollama ps`) | role |
|---|---|---|
| `qwen3.8:latest` | llamacpp, 19.6 tok/s on a 4-token probe | B1 incumbent family, version-bump control |
| `qwen3.8:27b-mlx` | mlx | the MLX hypothesis |
| `gemma4:26b` | llamacpp, 141 tok/s on a 4-token probe | B1's passing model, version-bump control |
| `gemma4:26b-mlx` | mlx | MLX comparison for a model that already passed |
| `bonsai-2:27b` | llamacpp | new: ternary 27B, 8.3 GB, Apache 2.0, tools+thinking |
| `dolphin-mistral:latest` | llamacpp | the live brain; B1 measured 105 ms TTFT but numbered-list replies |

`flint` (40B MoE, coding agent) is a worker candidate, not conversational; not
in this run. `nemotron-3.5-bolt` has no Apple Silicon support; excluded.

## Gate (the harness's, unchanged from B1)

Recorded in the artifact's `gate` block: TTFT p50 ≤ 2,000 ms, TTFT p95 ≤
4,000 ms (nearest rank on n=15, read as worst observed), generation ≥ 25
tok/s, worst turn ≤ 90 s, all four in both history conditions; plus the B1
behavioral checks (no leaked reasoning, no tool-call JSON, speech-shaped
replies). Passing is a precondition for being offered as a default, not a
decision; quality stays Lance's call.

*Correction before reading results:* the first draft of this file stated a
tighter gate (p95 under 1 s, worst under 15 s) from memory. The harness's gate
is the contract, so this section was aligned to it before the results were
read.

## What this run cannot say

- Nothing about reply quality beyond the B1 behavioral checks.
- One machine (M4 Max, 128 GB), one Ollama version, one persona prompt.
- `-rewarm dolphin-mistral:latest` leaves the live brain resident afterwards;
  during the run every candidate is unloaded in turn.
