# OpenAI-compatible speech (`openai-speech`)

`tts_provider: openai-speech` speaks through any server that implements the
OpenAI `POST /v1/audio/speech` endpoint and can stream raw PCM. It is an
additional engine next to Kokoro, Qwen3-TTS and audio8; Kokoro remains the
default. Nothing is downloaded or installed by obey-voice for this engine: the
server owns the model.

## Phonon through Ollama

Ollama 0.40.9 and later serve [Gradium's Phonon](https://ollama.com/library/phonon)
on Apple Silicon. Phonon 31M English is Apache 2.0 and 64 MB.

```bash
ollama pull phonon
obey-voice config set tts_provider openai-speech   # or Settings → Voice → Engine
```

The defaults already point at Ollama on this computer (`openai_speech_url`
`http://localhost:11434/v1`, `openai_speech_model` `phonon`,
`openai_speech_voice` `default`). Ollama exposes one Phonon voice, `default`.

Measured on an M4 Max through Ollama's MLX runner (2026-10-23, `curl` against
`/v1/audio/speech` with `response_format: pcm`): first byte in 4 to 15 ms warm,
a six-second reply fully synthesized in 65 to 128 ms. The first request after
Ollama unloads the idle model paid 2.4 s to reload it. For comparison,
obey-voice's measured Kokoro synthesizes one sentence in about 900 ms
(1.32x realtime; see `docs/benchmarks/N7-tts-bake-off-2026-08-12.md`). These
are endpoint timings. Through the real provider seam
([P1](benchmarks/P1-phonon-vs-kokoro-2026-10-23.md), same day): Phonon's first
PCM frame in 4 ms against Kokoro's 290 ms for one sentence, 78 to 92x realtime
against 4.1 to 4.5x. `just bench tts "kokoro openai-speech"` measures full turns.

## Any other server

Gradium's `ptts-openai-server` serves the same endpoint with Phonon's full voice
catalog and `GET /v1/audio/voices`; point `openai_speech_url` at it and the voice
pickers list what it offers. Set `openai_speech_api_key` for a server that wants
a bearer token.

## Behavior

- The client asks for `pcm` (16-bit little-endian mono, 24 kHz) and hands frames
  to the player as the body arrives, so playback can start before synthesis ends.
- Text goes through the same spoken-text cleaning as the other engines
  (think blocks, emoji, quotes). Ollama reads Markdown aloud and skips emoji,
  so the cleaning matters.
- One request is capped at 4,096 characters on Ollama. Longer text (`render`,
  audiobooks) is split at sentence boundaries and requested in order into the
  same stream.
- Start-up proves the server answers `GET /v1/models` and, when it lists
  models, that the configured one is present. A server that is not there is
  reported as unavailable with the URL and the `ollama pull phonon` remedy; the
  conversation does not silently switch engines.
- An unknown voice surfaces the server's own message, which on Ollama names the
  voices it has.
- `doctor` reports the configured target offline; it does not probe the server.

## Voices and personas

A persona may pin `tts_provider: openai-speech` and a voice; the voice lands in
`openai_speech_voice`, never in Kokoro's `tts_voice`. The Personas form and
Settings list the configured voice plus `default`; a server that publishes
`/v1/audio/voices` replaces that list with its catalog at start.
