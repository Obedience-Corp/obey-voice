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
retry) or `plain` (the reply). Each carries Ollama's `prompt_eval_count`,
`prompt_eval_ms`, `eval_count` and `total_ms` from the final response line.
Compare `correct` for routing accuracy and the `plain` request's
`prompt_eval_ms` for prompt-cache reuse. `prompt_eval_count` includes cached
tokens on Ollama's llama.cpp server backend, so it does not show reuse.
`-admission-only` does not apply to this comparison.
