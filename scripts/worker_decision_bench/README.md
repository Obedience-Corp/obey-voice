# Local worker decision replay

`just bench worker-decision -cases /path/to/cases.json -out /path/to/new.jsonl`
compares the real Ollama addressee resolver and `ThinkStream` route with a
decision started alongside the resolver. A ready, valid work proposal skips
`ThinkStream`. If it is late, unavailable or uncertain, routing continues
immediately. The command is a lab tool; it does not enable a voice feature.

The baseline and candidate run in alternating order for each case, with three
rotated passes by default. Separate decision and baseline warmups must succeed
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

Fixtures deliberately have no persona changes. An unexpected address result
ends that observation and cannot authorize a fast route. Exact control phrases,
live host policy changes, interruption, real worker admission and audio require
separate pipeline verification. The held-out corpus and results for WI-fb8f26
are retained in its campaign work item, outside the source repository.
