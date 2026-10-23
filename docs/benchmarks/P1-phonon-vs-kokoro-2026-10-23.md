# P1 — Phonon (openai-speech via Ollama) vs Kokoro, real provider seam

Date 2026-10-23 · machine Apple M4 Max, 128 GB · Ollama 0.40.9 (MLX runner) ·
engine commit on branch `phonon-openai-speech` (PR #239) · raw rows in
[`P1-phonon-vs-kokoro-2026-10-23.json`](P1-phonon-vs-kokoro-2026-10-23.json).

## Criteria, fixed before measuring

- Both engines are constructed through `ttsproviders.NewProvider` on the live
  config (read-only) and driven through `Provider.Synthesize`, the call the
  pipeline makes. No playback device is opened. Harness:
  `go test -tags integration ./pkg/voiceagent/tts/openaispeech/ -run TestOpenAISpeechVsKokoroRealtime -v`.
- Each engine is warmed with one utterance first; model load is not charged.
- Three fixtures, five runs each, medians reported: one sentence, two
  sentences (the N7 pair), and a 190-character long reply.
- **first frame** is time from `Synthesize` to the first PCM frame the player
  could start on. **synth** is time to stream close. **realtime factor** is
  audio seconds per synthesis second.
- The question this answers: is Phonon fast enough that its first-audio latency
  stops being a factor? It does not decide quality or default; Kokoro stays the
  default regardless (Lance, 2026-10-23).

## Results

| engine | case | first frame | synth | audio | realtime |
|---|---|---:|---:|---:|---:|
| kokoro (af_heart) | one sentence | 290.0 ms | 290.0 ms | 1186 ms | 4.1x |
| | two sentences | 656.6 ms | 656.6 ms | 2750 ms | 4.2x |
| | long reply | 2214.7 ms | 2215.2 ms | 9943 ms | 4.5x |
| openai-speech (phonon, voice default) | one sentence | **4.1 ms** | 25.5 ms | 2000 ms | 78.3x |
| | two sentences | **4.1 ms** | 43.2 ms | 3680 ms | 85.2x |
| | long reply | **4.0 ms** | 116.1 ms | 10720 ms | 92.3x |

## Reading the numbers

- Kokoro's first frame is its whole synthesis: 290 ms for one sentence, 2.2 s
  for a long reply. Phonon's first frame is the HTTP round trip, 4 ms, and
  the whole utterance is done 20 to 50 times sooner.
- Kokoro measured 4.1x realtime here against 1.32x in N7 (2026-08-12). The N7
  figure was taken on the same machine with the earlier sherpa build; this run
  does not explain the gap and should not be read as a Kokoro regression or
  improvement claim. The Phonon rows are the point of this artifact.
- Phonon speaks the same text more slowly (2.0 s vs 1.2 s for one sentence), so
  per-sentence audio is longer. That is pacing, not a cost.
- Cold start: Ollama unloads an idle model after a few minutes; the first
  request after that paid 2.4 s (endpoint probe, same day). The provider's
  first-audio grace floors at 5 s for that reason.

## Evidence limits

- Endpoint plus provider timing, not a full conversational turn. The pipeline's
  segment grouping, lookahead and backchannel were not exercised.
- One voice (`default`) and English only. No quality or WER measurement.
- Single machine, single Ollama version, five runs per case.

## Durable value

Phonon through Ollama removes synthesis from the first-audio budget on this
machine. Whether the pipeline should relax D009 (cross-sentence batching) or the
backchannel for this engine is a separate decision that needs a full-turn
measurement, which `just bench tts "kokoro openai-speech"` can provide.
