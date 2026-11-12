# OpenAI-compatible speech (`openai-speech`)

`tts_provider: openai-speech` speaks through any server that implements the
OpenAI `POST /v1/audio/speech` endpoint and can stream raw PCM. It is an
additional engine next to Kokoro, Qwen3-TTS and audio8. Kokoro is the compiled
default; builtin Obey, Samantha, Jarvis, Veronica and Dylan personas select Phonon.
The server owns the model. When the server is Ollama, obey-voice
installs it there and fetches Phonon's voice catalog file (see
[Install](#install)).

## Linux: managed native Phonon

With `openai_speech_runtime: auto` (the default), an unconfigured Linux Phonon
setup uses Gradium's native server. macOS continues using Ollama. Explicit
speech URLs, models and API keys preserve the external-server path; the
client's operating system does not determine a remote server's capabilities.

For an existing OpenAI speech persona that previously tried local Ollama on
Linux, opt into the native runtime:

```sh
obey-voice config set openai_speech_runtime native
obey-voice models ensure --tts
```

Setup verifies and builds a pinned Rust source archive, then provisions pinned
Phonon 31M English weights and the catalog's voice embeddings. On Arch, its
build prerequisites are:

```sh
sudo pacman -S --needed rust base-devel opus lame
```

CUDA also needs the CUDA toolkit and working NVIDIA driver; a Vulkan device
needs its Vulkan driver/loader. A toolkit installed outside the normal search
paths can be selected with `CUDA_ROOT`/`CUDA_PATH`, its `bin` directory on
`PATH`, and its shared libraries on `LD_LIBRARY_PATH`. The first source build
can take several minutes and requires network access. Setup errors include
the compiler/runtime's output; the Go executable can still be installed when
voice setup is incomplete.

`openai_speech_backend: auto` chooses CUDA when `/dev/nvidia0` exists, Vulkan
for Radeon/Intel ICD installations, otherwise CPU. Override that selection
with `cpu`, `vulkan` or `cuda`. The server receives an explicit device, and
startup verifies its actual device banner; an unavailable GPU is an error
rather than a silent CPU fallback. The native GPU path uses F32 weights;
upstream q8 weights are CPU-only.

Speech construction starts an owned server on a private loopback port and
stops it with the provider. Conversation startup uses cached assets offline;
explicit setup performs provisioning. `models status` and `doctor` inspect
cached readiness without starting or downloading a runtime. They distinguish
that cache check from the device verification performed at startup. All native
assets live below `<models_dir>/phonon/native`; model cleaning protects that
tree while an OpenAI speech provider/persona references it.

The managed model is `gradium/phonon-31m-en`. Harper, Juniper, Edwin and Zoey
remain their existing Phonon voices. Setup also verifies Fennick the Young Elf,
the catalog voice Dylan uses. The server fetches every English voice in the
pinned catalog during setup, so this adds no separate download. Optional
`openai_speech_native_binary` accepts an absolute path to a compatible native
server instead of building one; model/catalog pins and readiness checks still
apply. To use a custom model or
an operator-managed server, select `openai_speech_runtime: external` and its
URL/model. This also keeps Linux clients using a remote Mac Ollama server.

## Phonon through Ollama

Ollama 0.40.11 and later provide [Phonon's named voices](https://docs.ollama.com/capabilities/speech)
on Apple Silicon. The older 0.40.9 speech API still works with `default`.
Phonon 31M English is Apache 2.0.

```bash
obey-voice config set tts_provider openai-speech   # or Settings → Voice → Engine
obey-voice models ensure --tts                     # pulls phonon into Ollama, fetches voices.jsonl
```

The defaults already point at Ollama on this computer (`openai_speech_url`
`http://localhost:11434/v1`, `openai_speech_model` `phonon`,
`openai_speech_voice` `default`). The current model includes named English
voices alongside `default`, exposed through `/api/show`. The shipped Phonon
personas use Harper, Juniper, Edwin, and Zoey; all four were verified through
Ollama 0.40.11. Dylan uses Fennick the Young Elf, a character voice from the
same English catalog. Obey Voice lists the voices your server actually reports.
If it only lists `default`, update Ollama if needed and run voice setup again as
described below.

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

## Install

`obey-voice models ensure --tts` and choosing `openai-speech` under Settings →
Voice → Engine run this setup. Settings → Voice → **Install / refresh voices**
runs it again for an existing installation, with progress and cancellation
when Settings closes or another engine is selected. Opening Settings only
reads the catalog.

On the external/Ollama path, Ollama remains an external dependency: setup names the required version when
the running server is too old; it does not replace or restart an external
Ollama installation. After an Ollama update, run setup again.

Starting a conversation, `voices`, and `voices preview --ensure` only ensure
the model is present. They do not refresh installed models or fetch the
descriptive catalog, so existing default-voice speech keeps working offline.
On that path, explicit setup performs these additional voice-readiness checks:

1. Ask the target what it is. A base URL ending in `/v1` whose root answers
   `GET /api/version` is Ollama. Ollama older than 0.40.9 is refused with that
   version named. A server that answers nothing is an error naming the URL and
   `ollama serve`; one that answers 401 or 403 is reported as a rejected
   request naming `openai_speech_api_key`.
2. On Ollama, pull the model when `GET /api/tags` does not list it (`phonon`
   matches `phonon:latest`, the way Ollama resolves it). The pull streams
   through `POST /api/pull`; layer bytes are summed into the same `download`
   progress ticks every other asset reports, then `verify`, `install` and
   `done`; the percentage never runs backwards when a layer is announced
   late. For the official `phonon` / `phonon:latest` tag, setup also checks
   `/api/show`. An installed model with no named voices is refreshed once on
   Ollama 0.40.11+. An older runtime gets an update/restart message before any
   download. A ready model reports `skipped`. Custom models and pinned tags
   are never refreshed automatically. A failed voice-list request stops setup
   rather than triggering a download. After a pull, setup verifies named
   voices before reporting `done`; a still-empty catalog fails explicitly.
   Only a listing that answered can prove the model absent: when both
   `/api/tags` and `/v1/models` fail, ensure stops with "could not check
   whether phonon is installed" and the reasons, nothing is pulled, and
   status reports the model as unknown rather than missing. A pull that goes 60 s without a line from Ollama is abandoned with
   that said, not as a cancellation.
3. Fetch `voices.jsonl` from
   [huggingface.co/gradium/phonon-voices](https://huggingface.co/gradium/phonon-voices)
   into `<models_dir>/phonon/voices.jsonl`, through a temp file renamed into
   place, unless a valid one is already there. Valid means the first non-empty
   line is a JSON voice entry with a `name`: an empty body or an HTML page (a
   captive portal) is refused with the URL named, and such a file on disk
   counts as not installed. The file only describes voices, so a failed fetch
   is a warning on stderr and the asset closes as `skipped` with a `detail`
   saying why, not a failed install. A server that is not Ollama gets this
   step and step 1 only.

`--json` streams these as NDJSON ticks with the assets `phonon (Ollama)` and
`phonon/voices.jsonl`, the names `models status --json --tts` gives its rows
`tts.openai-speech.model` and `tts.openai-speech.voices`. For the official
Phonon tag, the model row is ready only when named voices can be listed, so
setup offers a repair even when an old model is already installed. The voices
row
carries `optional: true`: it is listed and `models ensure` fetches it, but the
human summary and the desktop setup's Models step leave it out of the missing
count, so a Phonon user without the catalog file is never held at an install
step; `doctor` still notes the missing file as a warning. When the target is
not Ollama, the model row is replaced by a `tts.openai-speech.server` row that
says whether it answers. Status never fails because the server is down; the
row says so. `models clean` keeps `<models_dir>/phonon` while the app or a
persona speaks through openai-speech.

`voices --json` and `voices preview` refuse with `assets_missing` when Ollama
answers and the model is not pulled. A missing `voices.jsonl` blocks neither.

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
- Start-up runs install steps 1 and 2 above, then proves the server answers
  `GET /v1/models` and, when it lists models, that the configured one is
  present. A server that is not there stops start-up with the URL and the
  `ollama serve` remedy; the conversation does not silently switch engines.
- An unknown voice surfaces the server's own message, which on Ollama names the
  voices it has. That is a request error and the turn fails rather than being
  spoken in the fallback voice. A server that is unreachable, answers 5xx,
  times out (408) or refuses the load (429) hands the sentence to
  `voice_fallback_provider` (Kokoro) when one is configured.
- `doctor` asks the target, in one probe bounded to 3 s: whether it answers,
  the Ollama release, whether the model is pulled, whether that release lists
  Phonon's voices in `/api/show`, and whether `voices.jsonl` is in
  `models_dir`. Each row carries its remedy (`ollama serve`,
  `obey-voice models ensure --tts`, or the Ollama release that lists voices).

## Voices and personas

A persona may pin `tts_provider: openai-speech` and a voice; the voice lands in
`openai_speech_voice`, never in Kokoro's `tts_voice`.

The catalog comes from the first of these that answers, and
`obey-voice voices --json` reports which in its `catalog` field:

| `catalog` | Where the names came from |
|---|---|
| `server` | `GET /v1/audio/voices`, which Gradium's `ptts-openai-server` publishes for the voice files it loaded |
| `ollama` | Ollama's `/api/show` `voices` list, shipped in [Ollama 0.40.11](https://github.com/ollama/ollama/releases/tag/v0.40.11) (the English catalog baked into `phonon`) |
| `configured` | Nothing was published: the configured voice plus `default` |

When the target is the official Phonon model and no named voices are listed,
the JSON carries a `notice` naming the required Ollama update or model refresh;
the Personas form shows
the same text under the Voice row. A non-Ollama server with no catalog has no
notice: one voice is simply what it has.

Descriptions ride along from Gradium's catalog file, which
`obey-voice models ensure --tts` puts at `<models_dir>/phonon/voices.jsonl`
(placing it there by hand works too). Every listed voice gains `locale`,
`gender`, `age` and `description`, matched by name in any case and with or
without accents (the way Ollama matches the `voice` field). The catalog is
ordered by locale, then name, so every surface shows one order; the
`--locale` and `--gender` filters apply to the merged attributes.

The Personas form asks the server for this catalog when it opens on
`openai-speech` (three HTTP calls, no model load) and offers a Language row to
narrow it; a voice typed for a server that did not list it is kept. Settings →
Voice asks the same way when Settings opens on `openai-speech` or the Engine
list is switched to it. Until the answer lands, or when the server cannot be
reached, the voice list is the configured voice plus `default`, and the Voice
heading says why. A Language filter row above the list narrows it to one
locale; voices that state no locale and the ✓ voice stay listed, and the
filter is a view, not a saved setting. The highlighted voice's locale, gender,
age and description show on the detail line. An unknown name is reported by
the server at the first synthesis, naming the voices it has.

To download only the optional voice descriptions (without installing speech or
persona models), run `obey-voice models ensure --voice-catalog`. Add `--json` for
streamed progress. This works while the speech server is offline; a failed
catalog fetch leaves speech readiness unchanged. Desktop setup offers the same
operation as **Download voice descriptions**, independently of **Continue**.
