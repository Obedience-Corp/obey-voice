# Addressee decision benchmark

Lab-only replay through `brain.OllamaBrain.ResolveAddressee` and a system-one HTTP candidate. It uses synthetic labeled `AddressRequest` inputs and does not change the installed voice configuration or dispatch tools. Results are JSONL, written to a new file and flushed after each observation.

```sh
just bench decision -cases /path/to/cases.json -out /path/to/baseline.jsonl -mode baseline
just bench decision -cases /path/to/cases.json -out /path/to/local-raw.jsonl -mode raw -budget 15s
just bench decision -cases /path/to/cases.json -out /path/to/local-hybrid.jsonl -mode hybrid
just bench decision -cases /path/to/cases.json -out /path/to/jev-raw.jsonl -mode raw -budget 15s -endpoint https://api.typesafe.ai/v1/systemone -model jev-latest -key-env TYPESAFE_API_KEY
```

Each case has `id`, `category`, `request`, and `want`. The request has the production `brain.AddressRequest` shape. `want` has `kind`, `name`, and `bare`; the lab-only `unavailable` kind means that production should identify an absent named target for host rejection. Other kinds are `none`, `address`, and `ambiguous`.

Defaults: three rotated passes, Samantha's `qwen3.8:latest` on local Ollama, context 32768, batch 128, a 750ms optional deadline, and a 16s parent deadline. The original resolver still enforces its own 15s limit. A separate warmup row has `rep: -1`; summaries exclude it but retain failed measurements. `raw` evaluates the candidate without fallback; `hybrid` measures an actual optional call followed by the real resolver when needed. HTTP call time includes transport, decoding, and validation.

The candidate's thresholds are experimental: Choice confidence 0.85, probability margin 0.20, and bare probability outside (0.10, 0.90). They are fixed in source so a result can be traced to an exact implementation. Do not infer calibration or general accuracy from the small corpus. This harness does not measure first audible speech, full pipeline admission, persisted persona selection, tool dispatch, or a talk-model ladder. Existing host tests must accompany these model observations.

The local default endpoint is `http://127.0.0.1:18763/v1/systemone`. Start the pinned decider server separately; this command does not install Python or manage a sidecar. The optional key is read only from the named environment variable and is not written into artifacts. Use the provider's own origin when supplying credentials. Synthetic fixtures can be sent to Jev; keep personal captures local unless explicitly authorized.

Run `go test -race ./scripts/decision_bench` for controlled HTTP failure, timeout, cancellation, abstention, and fallback checks. No live model calls run in those tests.
