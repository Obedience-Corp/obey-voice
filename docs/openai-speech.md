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

Measured on an M4 Max through Ollama's MLX runner, both engines through the
real provider seam, no playback device
([P1](benchmarks/P1-phonon-vs-kokoro-2026-10-23.md), 2026-10-23): Phonon's
first PCM frame arrives in 4 ms against Kokoro's 290 ms for one sentence and
2.1 s for a long reply; synthesis runs 53 to 87x realtime against Kokoro's 4.1
to 4.7x. The first request after Ollama unloads the idle model paid 2.4 s to
reload it. `just bench tts "kokoro openai-speech"` measures full turns.

Phonon pads every response with about half a second of trailing silence. The
provider trims it (keeping 80 ms) so sentence-by-sentence playback does not
gain dead air between sentences; pauses inside an utterance are kept.

## Any other server

Gradium's `ptts-openai-server` serves the same endpoint with Phonon's full voice
catalog and `GET /v1/audio/voices`; point `openai_speech_url` at it and
`obey-voice voices` lists what it offers. Set `openai_speech_api_key` for a
server that wants a bearer token.

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
  voices it has. That is a request error and the turn fails rather than being
  spoken in the fallback voice. A server that is unreachable, answers 5xx,
  times out (408) or refuses the load (429) hands the sentence to
  `voice_fallback_provider` (Kokoro) when one is configured.
- `doctor` reports the configured target offline; it does not probe the server.

## Voices and personas

A persona may pin `tts_provider: openai-speech` and a voice; the voice lands in
`openai_speech_voice`, never in Kokoro's `tts_voice`.

The catalog comes from the first of these that answers, and
`obey-voice voices --json` reports which in its `catalog` field:

| `catalog` | Where the names came from |
|---|---|
| `server` | `GET /v1/audio/voices`, which Gradium's `ptts-openai-server` publishes for the voice files it loaded |
| `ollama` | Ollama's `/api/show` `voices` list, present from the release that carries [ollama/ollama#19242](https://github.com/ollama/ollama/pull/19242) (the English catalog baked into `phonon`) |
| `configured` | Nothing was published: the configured voice plus `default` |

When the target is Ollama and its release lists no voices, the JSON carries a
`notice` naming the version and the upstream change; the Personas form shows
the same text under the Voice row. A non-Ollama server with no catalog has no
notice: one voice is simply what it has.

Descriptions ride along from Gradium's catalog file. Place `voices.jsonl` from
[huggingface.co/gradium/phonon-voices](https://huggingface.co/gradium/phonon-voices)
at `<models_dir>/phonon/voices.jsonl` and every listed voice gains `locale`,
`gender`, `age` and `description`, matched by name in any case and with or
without accents (the way Ollama matches the `voice` field). The catalog is
ordered by locale, then name, so every surface shows one order; the
`--locale` and `--gender` filters apply to the merged attributes.

The Personas form asks the server for this catalog when it opens on
`openai-speech` (three HTTP calls, no model load) and offers a Language row to
narrow it; a voice typed for a server that did not list it is kept. The
Settings voice list still shows the configured voice plus `default`; wiring
the live catalog into Settings is the next slice. An unknown name is reported
by the server at the first synthesis, naming the voices it has.
