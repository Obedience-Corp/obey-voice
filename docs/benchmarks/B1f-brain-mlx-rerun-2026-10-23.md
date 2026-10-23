# B1f — brain-model rerun on Ollama 0.40.9 (MLX): results

Criteria: [B1f-brain-mlx-rerun-criteria.md](B1f-brain-mlx-rerun-criteria.md).
Artifact: [B1f-brain-mlx-rerun-2026-10-23.json](B1f-brain-mlx-rerun-2026-10-23.json)
(`go run ./scripts/brain_bench -id B1f -models … -reps 3 -rewarm dolphin-mistral:latest`).
Apple M4 Max 128 GB, Ollama 0.40.9, `obey` persona, five prompts × three reps ×
two history conditions per model, models unloaded between candidates.

## Headline

1. **The qwen3.8 fixed cost B1 found is gone.** On llama.cpp qwen3.8's TTFT p50
   went from 4,637 ms (B1, Ollama 0.32) to 693 ms; on MLX it is 109 ms, with
   generation up from 28 to 47 tok/s. The version bump removed most of it and
   the MLX runner takes the rest. WI-adf58f's question is answered by the runtime,
   not by the prompt.
2. **Every model's p95 is one prompt.** P5 ("local or cloud model for voice?")
   activates a skill, which splices about 4.9k tokens into the request
   (2.3k → 7.2k empty; 5.8k → 10.6k filled). That prefill is the p95 for all six
   candidates and the only reason five of them fail the gate. It is a prompt-size
   cost of skill activation on this machine, not a model defect.
3. **`gemma4:26b-mlx` is the only candidate that passes** both conditions:
   TTFT p50 55/60 ms, p95 3.7/3.9 s (P5), 87/78 tok/s, worst turn 4.3 s.
4. **The live brain has a real failure.** `dolphin-mistral` does not support
   tools (Ollama returns 400; the agent runs without file read/write), and under
   filled history P5 ran away: a 14,371-character reply and a 97 s turn, past the
   90 s gate and within sight of the 180 s pipeline timeout that lost replies in
   August.

## Results

| model | runner | cold first turn | cond | TTFT p50 | TTFT p95 | tok/s | worst turn | gate |
|---|---|---:|---|---:|---:|---:|---:|:--:|
| qwen3.8:latest | llamacpp | 21.2 s | empty | 693 ms | 24,187 ms | 28.0 | 26.7 s | FAIL |
| | | | 6k | 771 ms | 25,170 ms | 24.4 | 29.5 s | FAIL |
| qwen3.8:27b-mlx | mlx | 13.8 s | empty | **109 ms** | 20,181 ms | 47.1 | 21.9 s | FAIL |
| | | | 6k | **114 ms** | 20,972 ms | 46.7 | 22.8 s | FAIL |
| gemma4:26b | llamacpp | 10.7 s | empty | 122 ms | 5,576 ms | 99.6 | 6.0 s | FAIL (p95) |
| | | | 6k | 134 ms | 6,121 ms | 97.1 | 6.7 s | FAIL (p95) |
| **gemma4:26b-mlx** | mlx | 6.3 s | empty | **55 ms** | **3,704 ms** | 87.2 | **4.1 s** | **PASS** |
| | | | 6k | **60 ms** | **3,912 ms** | 77.7 | **4.3 s** | **PASS** |
| bonsai-2:27b | llamacpp | 13.9 s | empty | 643 ms | 22,270 ms | 36.8 | 24.4 s | FAIL |
| | | | 6k | 659 ms | 23,049 ms | 35.7 | 26.0 s | FAIL |
| dolphin-mistral:latest | llamacpp | 4.1 s | empty | 106 ms | 8,603 ms | 92.0 | 11.4 s | FAIL (p95) |
| | | | 6k | 130 ms | 9,956 ms | 81.8 | **97.1 s** | FAIL (p95, worst) |

Per prompt, TTFT medians excluding P5 sit within a narrow band for each model
(qwen3.8 llamacpp 676–771 ms, qwen3.8 mlx 104–232 ms, gemma4 llamacpp 93–161 ms,
gemma4 mlx 53–73 ms, bonsai 620–692 ms, dolphin 46–176 ms). P5's TTFT is the
skill prefill: 24.0 s qwen3.8 llamacpp, 20.2 s qwen3.8 mlx, 5.5 s gemma4
llamacpp, 3.7 s gemma4 mlx, 22.2 s bonsai, 8.6 s dolphin. Zero think leaks on
any model; zero errors; zero turn timeouts.

Two more things the per-turn rows show:

- `qwen3.8:27b-mlx` made tool calls on P1 and P2 in some reps (the only model to
  do so), costing 9 to 15 s turns for "What's the capital of Australia?". That
  is a behavior to watch, not a latency defect.
- `bonsai-2:27b` (ternary, 8.3 GB on disk, 26 GB resident at 256K context) is
  no faster than qwen3.8 on llama.cpp here and slower on generation than gemma4.
  Nothing in this run argues for it.

## What this run cannot say

- Nothing about reply quality beyond the B1 behavioral checks; the P2/P4 replies
  read as speech on every model.
- The P5 prefill cost is specific to the current skills catalog size and to how
  `activate_skill` splices skill text. Whether to cap, cache or lazy-load that
  text is a product decision (a follow-up, not this run).
- One machine, one Ollama version, one persona prompt, n=15 per condition.

## Recommendation

Measurement supports `gemma4:26b-mlx` as the fastest conversational brain on this
machine and the only gate pass; Lance declined gemma4 in August on quality, and
nothing here changes that call for him. The actionable finding regardless of
model choice is P5: skill activation's prefill is now the whole p95, and
`dolphin-mistral`'s 97 s runaway under filled history is a live risk today.
