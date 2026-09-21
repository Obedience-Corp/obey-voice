# obey-transcript/v1 recorded streams

One file per scenario, exactly as `obey-voice transcribe --jsonl` writes it.
They are generated, not hand-written: the source is
`cmd/obey-voice/cmd/testdata/transcribe/golden/`, produced by
`go test ./cmd/obey-voice/cmd -run TestTranscribeConformanceFixtures -update-transcribe-golden`.

**Source commit:** `8aff191ac8955dc466d2c9c4eac40c1c2d32bdf0` (`transcribe-command` HEAD when these files were copied here). That commit contains the `obey-voice transcribe` command these streams were recorded against (the command itself landed in `a525016adeb60d34858f003ef5c8532660c7bc71`). The JSONL bytes were copied from `cmd/obey-voice/cmd/testdata/transcribe/golden/` in this worktree; they are not in `8aff191a` itself until that testdata is committed.

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
