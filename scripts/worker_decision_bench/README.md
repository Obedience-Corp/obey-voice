# Local worker decision replay

`just bench worker-decision -cases /path/to/cases.json -out /path/to/new.jsonl`
compares the real Ollama addressee resolver and `ThinkStream` route with a
decision started alongside the resolver. A ready, valid work proposal skips
`ThinkStream`. If it is late, unavailable or uncertain, routing continues
immediately. The command is a lab tool; it does not enable a voice feature.

The baseline and candidate reverse order for each case on successive passes,
with four rotated passes by default. An even pass count balances which mode
runs first for every case. Separate decision and baseline warmups must succeed
before measurement. The new output file contains the corpus/question hashes,
settings, warmups and every observation, including failures. A loopback proxy
counts actual Ollama `/api/chat` requests, including retries. `first_text_ms`
measures the first speakable text chunk; it is **not first audible response**.
Total time ends at a proposed work route or completed chat response, not worker
admission, worker start or audio playback.

Only literal loopback HTTP Ollama origins and a loopback decision endpoint are
accepted. Default endpoints are `127.0.0.1:11434` and `127.0.0.1:18763/v1/systemone`.
No user configuration is loaded. A temporary empty work/prompt directory and
the embedded Samantha persona isolate the synthetic replay. The speaking brain
uses the real tiered route-only tool session, which intercepts worker tools.
Delegation and skills are disabled. No Agency, job queue or worker is created.

Each JSON case has this shape:

```json
{
  "id": "unique-name",
  "input": {
    "latest_user_message": "Check the installed Python version.",
    "recent_conversation": [],
    "enabled_worker_tiers": ["simple", "medium", "complex"]
  },
  "want_work": true,
  "want_tier": "simple"
}
```

A case without `want_work` expects chat; a `chat` route has an empty `route`.

Fixtures deliberately have no persona changes. An unexpected address result
ends that observation and cannot authorize a fast route. Exact control phrases,
live host policy changes, interruption, real worker admission and audio require
separate pipeline verification. The held-out corpus and results for WI-fb8f26
are retained in its campaign work item, outside the source repository.

Use `-admission-only` to isolate concurrent decision inference from speaking
prompt/cache changes. Both modes still call the real addressee resolver; no
proposal is adopted and no speaking model request is issued. Its manifest
explicitly marks that narrower scope. Use `-start-rep N` with a fresh output file
to extend a retained comparison; do not overwrite or silently merge raw runs.

## Router comparison

`just bench worker-decision -compare router -cases /path/to/cases.json -out /path/to/new.jsonl -model <model>`
runs each case once with the live `route` tool (`route_only`) and once with the
tools-free JSON route (`json_route`), using the same case file. It makes no
addressee or decision-model call and needs no decision endpoint, so every
recorded request belongs to the turn. The order of the two modes alternates per
case and pass, as above.

Each sample lists its Ollama requests under `requests`, in order. `kind` is
`tools` (the route tool was offered), `schema` (a JSON route or the recovery
retry) or `plain` (the reply). Two `schema` requests in a `json_route` sample
mean the first decision was rejected and the recovery call ran. Each carries
Ollama's `prompt_eval_count`, `prompt_eval_ms`, `eval_count` and `total_ms`
from the final response line. Compare `correct` for routing accuracy and the
`plain` request's `prompt_eval_ms` for prompt-cache reuse. `prompt_eval_count`
can include cached tokens (the llama.cpp server backend counts them in the
Ollama source this repository builds against; the MLX runner is unchecked), so
it may not show reuse. The speaking brain runs with voice tools on, so its
system prompt carries the same live route guidance as a real turn in both modes.
`-admission-only` does not apply to this comparison.

`-think true` (or another `ollama_think` value) runs the speaking model with
thinking on, and the manifest records it. Without it thinking is off. The first
run on real models must include at least one model with `-think true`:
`json_route` keeps the turn's thinking setting on its schema-constrained route
request, and nothing has shown how a thinking model behaves there (see
"`json_route` with thinking models" in `docs/configuration.md`).

### Run order and its bias

Each sample records `order`: `first` or `second` within its case's pair. The
run ends with a `router_summary` line, printed too, that gives each mode's
means at each position: elapsed time over samples without an error, and first
text and the reply's `prompt_eval_ms` over the samples that spoke a reply.
`balanced` is false when `-reps` is odd.

The two modes run back to back on one case with the same system prompt,
history and user message, and nothing clears Ollama's cache between them. The
second mode's first request can therefore reuse the prefix the first mode just
read, so `second` samples start warmer than `first` samples. The first mode
starts after a different case or the warmup, sharing at most the system prompt.
Alternating the order cancels this in the overall means only with an even
`-reps`, and only on average. Compare modes at the same position, `first`
against `first`, rather than overall means. Even then neither position matches
a real conversation, where the previous turn already read the same history.
The reply's `prompt_eval_ms` is the least affected figure, since the request
before it is the same mode's route request, but it is not immune.

The Ollama API in the source this repository builds against has no call that
clears the prompt cache short of unloading the model.
Unloading between modes would make every first request pay a model load and an
empty cache, which no voice turn does, so the bench does not do it. The warmup
runs `route_only` only.
