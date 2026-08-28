# Audio8 tokenizer parity harness

Regenerates every artifact the pure-Go BPE (`pkg/voiceagent/tts/hfbpe`) is
tested against. Nothing under `hfbpe/testdata/` is hand-written: the expected
ids all come from the HuggingFace `tokenizers` library reading the checkpoint's
own `tokenizer.json`.

## Setup

Same snapshot and venv as the benchmark harness — see
[`../audio8_bench/README.md`](../audio8_bench/README.md).

```sh
export AUDIO8_MODEL_DIR=<hf cache>/models--Audio8--audio8-TTS-0.1B-ONNX-INT8/snapshots/e1c07e8a3725077e3ab80ad8578e5787e8a23c6c
python -m pip install 'tokenizers==0.23.1'
python gen_parity.py
```

| File | What it is |
|---|---|
| `corpus.py` | the texts, grouped by what they stress (prompt fragments, punctuation, numbers, scripts, emoji, whitespace, added tokens, symbols) |
| `gen_parity.py` | writes the four artifacts below and re-checks the integration fixture |

| Artifact | Used by |
|---|---|
| `hfbpe/testdata/audio8_parity.json` | `TestAudio8ParityCorpus` — the full-vocabulary gate. Needs the 5.8 MB `tokenizer.json`, which is not vendored, so it skips without `AUDIO8_MODEL_DIR` |
| `hfbpe/testdata/mini_tokenizer.json` + `mini_parity.json` | `TestMiniTokenizerParity` — a small tokenizer trained here with the checkpoint's exact pre-tokenizer sequence and added-token shapes, so the whole pipeline stays under reference-generated test on every `go test` |
| `hfbpe/testdata/punctuation_codepoints.json` | `TestPunctuationTableMatchesReference` — pins `punctuation.go` to the reference's own P* table |

`gen_parity.py` also re-encodes every entry of
`pkg/voiceagent/tts/testdata/audio8_tokenizer_fixture.json` and fails if the
checked-in integration fixture disagrees with the reference: the fixture the
DualAR test replays and this corpus have to be the same tokenizer.

## Running the Go side

```sh
go test ./pkg/voiceagent/tts/hfbpe/ -count=1                     # mini parity, rejections, punctuation table
AUDIO8_MODEL_DIR=... go test ./pkg/voiceagent/tts/... -count=1   # + the full corpus and the fixture round-trip
```
