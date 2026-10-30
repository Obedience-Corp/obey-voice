# brain_bench — conversational brain-model A/B

Measures how long a local Ollama model takes to answer a spoken turn, **through
obey-voice's real brain seam** rather than against the raw Ollama API.

- **Criteria (written before any measurement):** [`docs/benchmarks/B1-brain-latency-ab-criteria.md`](../../docs/benchmarks/B1-brain-latency-ab-criteria.md)
- **Results:** `docs/benchmarks/B1-brain-latency-ab-<date>.json` + the sibling `.md`

## Why not curl

A one-line prompt against `/api/chat` measures a request nobody makes. The real
request carries the persona document, the environment grounding block, the Agent
Skills catalog, the turn instruction, seven tool schemas, and a conversation in
front of it. Prefill cost is where large models lose, and a bare request has
almost no prefill — so a curl benchmark makes every candidate look good and ranks
them wrongly.

This harness therefore builds a real `*brain.OllamaBrain` and drives
`ThinkStream`, which is the same call the pipeline makes:

```
config.LoadRaw()                     # live config, read-only
persona.Load(id) + persona.Apply()   # persona overlay, in memory
brain.NewOllama(cfg)                 # real system prompt, skills, keep_alive
b.ThinkStream(ctx, input, StreamOptions{VoiceMode: true, ToolsEnabled: …})
```

`LoadRaw` rather than `Load` is deliberate: `Load` runs the persona `afterLoad`
hook, which is permitted to write migration files. This benchmark reads `~/.obey`
and writes nothing, so it applies the persona overlay explicitly instead.

## Run it

```bash
# Full run: nine candidates, both history conditions, 3 warm reps each.
go run ./scripts/brain_bench

# One model, quick.
go run ./scripts/brain_bench -models "qwen3:30b" -reps 1

# Include a model the criteria excluded.
go run ./scripts/brain_bench -models "nemotron3:33b,qwen3.6:latest"
```

| Flag | Default | Meaning |
|---|---|---|
| `-persona` | `obey` | Persona profile whose prompts and stack are applied |
| `-models` | the nine candidates | Comma-separated model list |
| `-reps` | `3` | Measured warm repetitions per prompt per condition |
| `-history-tokens` | `6000` | Target estimated prompt size for the filled condition |
| `-turn-timeout` | `3m` | Per-turn deadline; mirrors `defaultBrainTurnTimeout` |
| `-workdir` | cwd | Agent working directory (affects environment block + skills discovery) |
| `-tools` | `config` | `config` \| `on` \| `off` — tool schemas in the request |
| `-stop-between` | `true` | Unload each model first, so cold-load numbers are real |
| `-rewarm` | `qwen3.8-uncensored:27b` | Model left resident at the end; empty to skip |
| `-out` | dated path under `docs/benchmarks/` | Artifact path |

Re-running with the same flags reproduces the artifact. Machine, commit, worktree
cleanliness, Ollama version, the effective request options, the system-prompt
hash, and the full installed-model list are all recorded in it, so a reader can
tell whether their re-run is comparable.

## What it measures

| Field | From | To |
|---|---|---|
| `route_ms` | `ThinkStream` called | `ThinkStream` returns (skill routing done) |
| `ttft_ms` | `ThinkStream` called | first streamed chunk |
| `total_ms` | `ThinkStream` called | stream closed |
| `gen_tok_s` | — | `eval_count / (total_ms − ttft_ms)` |
| `prefill_tok_s` | — | first request's `prompt_eval_count / ttft_ms` |

Token counts come from Ollama's own metrics through the `OnUsage` hook, not from
an estimate. `prompt_tokens` is the **first** request's `prompt_eval_count`; a
tool loop issues more requests, which are summed separately into
`prompt_tokens_all_requests` so a tool detour cannot silently inflate the prompt
size the turn started from.

### Two things that arrive on the same callback

`OnToolStart` fires both for the semantic skill router (`activate_skill`, which
splices skill text onto the user message) and for a real tool invocation (which
costs a whole extra model round trip). The harness separates them into
`skills_activated` and `tool_calls`, and records the tool names. Conflating them
would hide the more interesting one.

### Conditions

- **`empty_history`** — `ClearHistory()`. System prompt + tool schemas + one turn.
- **`filled_history_6k`** — a realistic prior conversation (`history.go`) loaded
  via `LoadHistory`, tail-trimmed to the token target.

History is reset to the condition's baseline before **every** sample, so prompt
size is constant across reps instead of growing.

Each condition begins with **one discarded turn**. Without it exactly one measured
sample pays an uncached prefill of the whole history while every later sample
reuses the server's KV prefix cache — and that lone outlier lands on the p95. A
real conversation keeps its prefix cached and re-reads only the new turn, so the
cached state is the honest one; the discard is how the run reaches it rather than
straddling both.

### `clean_turns_summary`

Some models fire a tool on plain conversation — `web_search` on "what's the
capital of Australia?". That is a real cost the user waits through, so the **gate
runs on the full summary**. `clean_turns_summary` covers only the turns with no
tool call, so the model-to-model comparison can also be read without an external
HTTP request in the middle of it. For a model that never reaches for a tool, the
two summaries are identical.

## Machine discipline

This is written to run on a live workstation against a live Ollama server.

- Serialized: one model at a time.
- Each candidate is **unloaded** before it is measured, so cold-load is real.
  Unload, not delete — `keep_alive: 0`; the model files are untouched.
- `~/.obey` is read, never written.
- Nothing is pulled and nothing is deleted.
- The incumbent is re-warmed at the end (`-rewarm`), so the next real
  conversation does not pay a cold load caused by the benchmark.

## What it does not measure

Response quality. Every reply is stored verbatim in the artifact so a human can
read them, and that is the only quality signal here on purpose — a score would
be theater. Whether a model is good to talk to is a judgment made in real use.

## Sharing output

Saved JSON is redacted by default: numerical measurements remain original, while
raw replies, spoken text, diagnostics and tool arguments are withheld and local
paths substituted. Counters describe pre-redaction text. No raw-output option is
provided by this harness; use a separately reviewed private diagnostic run when
text-level investigation is necessary. Console output remains private and must
also be reviewed before sharing. See [sharing artifacts](../../docs/benchmarks/README.md#sharing-artifacts).
