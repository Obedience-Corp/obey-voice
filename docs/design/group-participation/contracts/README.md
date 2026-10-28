# Executable design contracts

- `decisions.schema.json`: strict audience, next-speaker and private-participant
  output shapes. Their cross-field, membership and delivered-reference constraints
  are checked by `verify.py` and must also exist in production host admission.
- `host-state.schema.json`: internal ticket/state and saved settings shapes.
- `examples.json`: valid decision examples.
- `audience-cases.json`: 15 initial semantic cases; despite an original case ID
  containing `heldout`, this set was inspected during prompt iteration and must
  not be reported as blind validation.
- `unseen-audience-cases.json`: six additional phrasings authored after v2 prompt
  was fixed; run once without tuning that prompt on their results.
- `policy-traces.json`: seven expected scheduling witnesses for later real
  pipeline replay. Offline checks validate witness invariants only, not scheduling.
- `commit-cases.json`: 15 revision/deadline/cancellation/budget examples. The
  reference predicate validates specification consistency; replay them against
  actual pipeline/Selector boundaries during implementation.
- `build_fixtures.py`: reproducibly authors schemas, initial examples/cases and
  trace/fence witnesses. It does not overwrite the separate unseen corpus.
- `prompts.json`: current experimental private prompts, exported from `probe.py`.

From the workitem directory:

```sh
python3 contracts/build_fixtures.py
uv run --with jsonschema==4.26.0 python contracts/verify.py
uv run --with jsonschema==4.26.0 python contracts/verify.py \
  --probe evidence/audience-qwen38-v2.json --report evidence/contract-results-v2.json
```

`verify.py --probe` exits nonzero for contract or expected-output failures. Initial
v1 probe failures are retained as negative evidence, not “fixed” recordings.
The command downloads/caches the Python validator through uv if absent; it adds
no project dependency. JSON Schema draft 2020-12 is explicitly declared.

To run a new local text experiment, using an already installed model:

```sh
python3 contracts/probe.py --model qwen3.8:latest --audience-only \
  --output evidence/audience-new-run.json
```

Use a fresh output name to preserve evidence. The script posts synthetic text to
loopback Ollama only, disables tools/thinking and uses temperature 0, seed 42,
8192 context and 256 max output tokens. It does not load personal app config,
persist conversations, open microphone/speaker, download models or invoke workers.
It writes prompts and raw outputs plus server token/timing counters. Runs share
the user's local inference resources; recorded timings are transport observations.

Without `--audience-only`, the probe also calls the current source's actual
addressee instruction and compares 2/3/4-request strategy-shaped workloads. The
original v1 selected workload always checked p0 even when another persona was
selected: its timings establish call shape only, not equivalent scheduling
quality. The current script follows the selected key. Fixed order, prompt cache
and lack of repeated measurements prevent interpreting either as a fair benchmark.

These schemas/fixtures are not a replacement scheduler or a new convoflow dialect.
Production tests must adapt them through existing isolated runtime/terminal
harnesses; today's convoflow runner does not consume this custom corpus directly.
