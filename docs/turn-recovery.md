# Turn recovery and token usage

[Back to README](../README.md)

A hard tool or brain failure always ends with a spoken recovery reply ("I hit an
error while working on that…") while the error detail goes to the activity feed;
the turn is reported as `completed (degraded)` rather than dropped silently.

For Ollama, Activity records per-request `prefill N tok · gen M tok`. Prefill
tracks the size of your new turn, not the whole transcript, and is bounded by
`ollama_num_ctx` (default `8192`). Startup warmup sends that same `num_ctx` —
omitting it lets Ollama allocate the model's full window (262k on qwen3/kimi)
and a serve restart loop will pin a runner per spawn. `ollama_num_batch`
(default `128`) decides how much of the prompt is re-read each turn. Ollama
otherwise derives it mainly from the context window — 512 at or below 4096, 1024
above, 2048 above 32768, downgraded toward 512 if the model's predicted VRAM
will not fit — and llama.cpp keeps its last reusable checkpoint about one batch back
from the end of the prompt, so a large batch means a conversational turn reuses
almost nothing and re-reads the persona and history from scratch. On
`qwen3.8-uncensored:27b` that cost 4.3 s before every spoken word; `128` skips
89 % of the prompt instead of 14 % and cut the median first token to 864 ms
(`docs/benchmarks/B2-qwen-ttft-mechanism-2026-08-28.md`). Brand-new prompts read
slightly slower, so this trades the tail for the median. `0` restores Ollama's
own choice. `ollama_keep_alive`
(default `10m`) keeps the
model resident between turns; `ollama_think` (default `auto`) turns thinking
on for qwen3-family models so chain-of-thought is not spoken. Explicit `false`
on qwen3 dumps untagged CoT into Content, which can be spoken.

See [Configuration](configuration.md) for the matching keys.
