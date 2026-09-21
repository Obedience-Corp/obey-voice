# obey-transcript/v1 recorded streams

One file per scenario, exactly as `obey-voice transcribe --jsonl` writes it.
They are generated, not hand-written: the source is
`cmd/obey-voice/cmd/testdata/transcribe/golden/`, produced by
`go test ./cmd/obey-voice/cmd -run TestTranscribeConformanceFixtures -update-transcribe-golden`.

**Source commit:** `d529faf183292caf01c837060f6ed7bafaa6c624`. That commit contains both the command behavior recorded here and the source golden files under `cmd/obey-voice/cmd/testdata/transcribe/golden/`.

`{{INPUT}}` and `{{MODELS_DIR}}` stand in for absolute paths, which differ on
every machine. A consumer substitutes its own.

| File | Scenario | Terminal event | Exit |
|---|---|---|---|
| `eof_speech.jsonl` | two utterances, the second ending at the file's last sample | `completed`, `speech` | 0 |
| `no_speech.jsonl` | 30 s of silence | `completed`, `no_speech` | 0 |
| `cancelled.jsonl` | interrupted after one segment | `cancelled` | 130 |
| `bad_version.jsonl` | a request naming a later major version | `failed`, `unsupported_protocol` | 2 |
| `unsupported_timing.jsonl` | `timing: word` | `failed`, `unsupported_capability` | 2 |
| `unsupported_language.jsonl` | a language the configured model does not transcribe | `failed`, `unsupported_capability` | 2 |
| `wrong_rate.jsonl` | a 48 kHz file described as 16 kHz | `failed`, `input_format_mismatch` | 2 |
| `missing_model.jsonl` | the model assets are not installed | `failed`, `model_missing` | 3 |

## Refreshing

A change to the protocol changes these files. Regenerate them with the command
above, copy them here, update the source commit to the commit that contains the
command behaviour just recorded, and tell the consumers: the Keepshot copies
under `src/transcript/testdata/` are refreshed by hand, and that project's fake
transcriber replays them.
