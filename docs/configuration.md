# Configuration

[Back to README](../README.md)

Config lives at `~/.obey/agents/voice/festival-voice/config.yaml`. Values can also be overridden with environment variables where listed.

`obey-voice config set <key> <value>` rewrites only that key's line: comments,
blank lines and key order survive, a timestamped `.bak` is kept (newest five),
and the replacement is atomic. It is the writer the TUI and the Obey Voice Mac
app both use, so three front ends cannot clobber each other's edits.

The value is read according to the key's type, which `obey-voice config schema`
publishes along with the bounds, accepted values, help text, and whether a
change needs an agent restart. A `list<string>` value is a JSON array:

```bash
obey-voice config set skills_disabled '["pdf-fill","calibre"]'
obey-voice config set skills_disabled '[]'
```

`obey-voice config unset <key>` is the way back: it removes the key's lines from
config.yaml — as surgically as `set` writes them — so the built-in default, or
the key's environment variable, applies again and `config get` reports the
value's source as `default`. Removing a key the file does not hold changes
nothing and reports `changed: false`.

Clearing a key is not the same as writing an empty value to it. `config set
stt_mode ""` stores an empty string, which pins the key; `config unset
stt_mode` removes it, so a future default change or an exported `STT_MODE` is
free to take effect.

`config schema` marks every key that accepts an empty value `allows_empty:
true`: all the text keys, plus the enums whose own default is blank
(`stt_mode`, `meeting.route.default`, `qwen_tts_mode`, …). A bool, a number, a
list, or an enum with a real default (`tts_provider`) refuses one. For the
enums, blank *is* the unset state, so writing it back returns the key to it —
a front end offering an "(App default)" choice wants `allows_empty` together
with an empty `default`.

`config schema` and `config get` never write to the install root, and never
fail on a config the loader would reject. Add `--json` to any of them for a
machine-readable payload; `config set` and `config unset` exit 0 on success, 1
when the operation fails (with an error `code` in the payload), and 2 when
called with the wrong number of arguments.

Persona **profiles** live under `~/.obey/agents/voice/festival-voice/personas/<id>/persona.yaml`.
On load, the active profile overlays `agent_name`, the persona prompt name, and per-persona TTS
(`tts.provider` + `tts.voice` → `tts_provider` and either `tts_voice` or `qwen_tts_voice`).
Each persona can use any supported TTS backend and any voice for that backend.
Prompt bodies stay in `prompts/` (see `obey-voice prompts`).

Shipped personas live in the catalog (`internal/persona/shipped/*.yaml` plus a
matching `prompts/defaults/persona.<id>.yaml`). Adding a companion is those two
files; there is no Go special case. **obey** is the default a fresh install
talks to and pins nothing — no brain, no TTS — so it speaks with whatever the
config above says. **samantha** and **veronica** are selectable companions
(Kokoro / VeronicaDesk). None of them can be deleted.

On every load, empty catalog slots are filled and occupied slots are left
alone. A fresh install starts on `obey`. An install that already named
`active_persona` keeps that choice. A user-authored profile at a shipped id
(`obey`, `veronica`, …) is never overwritten.

`builtin: true` in a profile is a provenance record — it marks a file this
product wrote, and it is what makes a profile undeletable. A persona you create
is never marked builtin, including one you name `obey`: a persona of your own at
a shipped id keeps its own prompt, pins and deletability, and is only activated
when you select it.

> **Upgrading:** installs that saved settings while `claude_max_session_tokens` defaulted to `60000` still carry that value in `config.yaml` and keep the silent session drops. Delete the key (or set `0`) to adopt the warn-only default. `obey-voice doctor` flags this.

| Key | Default | Environment | Description |
|-----|---------|-------------|-------------|
| `active_persona` | `obey` | `ACTIVE_PERSONA` | Persona profile id under `personas/<id>/` |
| `agent_name` | `Obey` | | Display name (overlaid from active persona) |
| `user_display_name` | empty | | Your name in prompts and chat labels. Empty keeps `speaker-1` and `{user_name}` as the OS login |
| `persona` | `obey` | `PERSONA` | Prompt document name for kind=persona (overlaid from active persona) |
| `compact_prompt` | *(embedded default)* | `COMPACT_PROMPT` | Prompt document name for kind=compact — the `/compact` summarize-turn instruction |
| `brain_provider` | `ollama` | `BRAIN_PROVIDER` | Brain backend: `ollama`, `claude`, or `grok` |
| `ollama_model` | empty | `OLLAMA_MODEL` | Ollama model name |
| `ollama_embedding_model` | `nomic-embed-text` | `OLLAMA_EMBEDDING_MODEL` | Ollama embedding model used to match each user prompt to relevant Agent Skills. Set empty to disable semantic routing and retain model-driven `read_skill` fallback. |
| `skills_similarity_threshold` | `0.55` | `SKILLS_SIMILARITY_THRESHOLD` | Minimum cosine similarity for automatic skill activation. Tune when using an embedding model with a different score distribution. |
| `ollama_host` | `http://localhost:11434` | `OLLAMA_HOST` | Ollama server URL |
| `ollama_num_ctx` | `8192` | `OLLAMA_NUM_CTX` | Context window requested on every chat call. `0` uses the server model default, which silently truncates long prompts from the top (system prompt first). |
| `ollama_num_batch` | `128` | `OLLAMA_NUM_BATCH` | How many prompt tokens Ollama re-reads per pass. Unset, Ollama picks it mainly from the context window (512 at or below 4096, 1024 above, 2048 above 32768), downgrading toward 512 if the model's predicted VRAM will not fit — it weighs the model's memory footprint but not its architecture. Values above `ollama_num_ctx` are clamped to it at load. This is a prefix-**reuse** control, not a speed one: llama.cpp keeps its last reusable checkpoint about one batch back from the end of the prompt, so a smaller batch skips more of an unchanged conversation (14% skipped at 1024 vs 89% at 128 on `qwen3.8-uncensored:27b`, 4.4 s to 0.8 s) while reading brand-new prompts slightly slower. `0` restores Ollama's choice. See `docs/benchmarks/B2-qwen-ttft-mechanism-2026-08-28.md`. |
| `ollama_keep_alive` | `10m` | `OLLAMA_KEEP_ALIVE` | How long Ollama keeps the model resident between turns (Go duration; empty = server default). |
| `ollama_think` | `auto` | `OLLAMA_THINK` | Ollama thinking/reasoning for models that support it (qwen3.x, …). `auto` turns thinking on for qwen3-family models so chain-of-thought lands in `Message.Thinking` instead of spoken Content. Explicit `false` is the leaky hatch: qwen3 then dumps untagged CoT into Content, which can be spoken and recovered as the `Obey:` line. Set `true` or `low`/`medium`/`high`/`max` to force thinking on any model. |
| `claude_max_session_tokens` | `0` | `CLAUDE_MAX_SESSION_TOKENS` | Opt-in fuse: caps the prompt the `claude` CLI replays on `--resume`. Past it the brain starts a fresh CLI session from recent history. `0` (default) trusts the CLI session and resumes forever — recommended for interactive use; set a cap for unattended/serve setups. |
| `claude_session_warn_tokens` | `60000` | `CLAUDE_SESSION_WARN_TOKENS` | Replayed-prompt size at which a visible warning appears (log + activity feed), once per CLI session. Nothing is dropped. `0` disables the warning. |
| `voice_tools_enabled` | `true` | `VOICE_TOOLS_ENABLED` | Local tools (`list_files` / `read_file` / `write_file` / `run_command` / `web_search` / `fetch_url` for Ollama). Default on; set `false` to disable. Switching brain provider to Ollama also re-enables when the key was dumped false. Remote `obey-voice serve` still uses `remote_tools_enabled` (default off). |
| `tool_command_timeout` | `30` (clamped 1–120) | `TOOL_COMMAND_TIMEOUT` | Maximum seconds for one local `run_command` invocation. The whole brain turn has its own timeout. |
| `remote_tools_enabled` | `false` | | Allow network-triggered turns from `obey-voice serve` to invoke tools; keep off unless remote clients are trusted. |
| `calibre_enabled` | `false` | `CALIBRE_ENABLED` | Opt in to Calibre library browse/search, TUI Library + picker, and `--from-library` |
| `calibre_library_path` | empty | `CALIBRE_LIBRARY_PATH` | Calibre library path (empty uses Calibre's default library) |
| `calibredb_binary` | empty | `CALIBREDB_BINARY` | `calibredb` path; empty uses PATH then macOS app bundle / `/opt/calibre` |
| `calibre_convert_binary` | empty | `CALIBRE_CONVERT_BINARY` | `ebook-convert` path used to convert MOBI/AZW-family books to EPUB |
| `calibre_prefer_format` | `epub` | `CALIBRE_PREFER_FORMAT` | Preferred book format when resolving (`epub`, `pdf`, then convertible MOBI/AZW-family formats) |
| `tts_provider` | `kokoro` | `TTS_PROVIDER` | TTS backend: `kokoro`, `qwen3-tts`, or `audio8` (experimental quality preview; needs the Audio8 export under `models_dir/audio8-tts`) |
| `voice_fallback_provider` | `kokoro` | `VOICE_FALLBACK_PROVIDER` | One-sentence runtime fallback after the selected provider fails; set empty/disabled to turn it off |
| `tts_voice` | `af_heart` | `TTS_VOICE` | Kokoro voice name |
| `audio8_voice` | empty | `AUDIO8_VOICE` | Which Audio8 voice speaks. Empty uses the reference packaged with the export; otherwise a voice registered with `obey-voice voices register` (see [Audio8 voices](audio8-voices.md)). Not an enum: registered names are yours, so an unknown one is reported by the provider rather than by `config set`. Switching voices re-pays the ~1.0 s prefix prime |
| `tts_place` | `local` | | Where Qwen speech runs: `local` (this computer) or `lan` (a `speech-serve` host — see [speech-serve.md](speech-serve.md)). Host topology, never a persona field |
| `tts_worker_url` | empty | | Speech worker address when `tts_place: lan`, e.g. `https://studio:7263`. `http://` only to loopback; never carries the token |
| `tts_worker_token` | empty | `OBEY_SPEECH_TOKEN` | Shared secret the speech worker requires. **Required** whenever `tts_place: lan` — a missing token is refused at validation, not at connect time. `config get` reports only whether it is set |
| `tts_lan_strict` | `false` | | Refuse to start a conversation when the speech worker is unreachable, instead of speaking with Kokoro here |
| `tts_lan_timeout_ms` | `200` | | Dial and handshake budget for the speech worker. It never bounds the worker's model load |
| `speech_speed` | `0.95` | | Playback speed |
| `qwen_tts_binary` | empty | `QWEN_TTS_BINARY` | Empty uses the native package worker under `models_dir/qwen3-tts`; set with `qwen_tts_model` for an explicit external worker |
| `qwen_tts_model` | empty | `QWEN_TTS_MODEL` | Empty uses the native package model dir; otherwise an external worker model directory |
| `qwen_tts_model_tier` | `0.6b` | `QWEN_TTS_MODEL_TIER` | Preferred native tier (`0.6b` or `1.7b` when present) |
| `qwen_tts_native_url` | platform release | `QWEN_TTS_NATIVE_URL` / `OBEY_VOICE_QWEN_NATIVE_URL` | Empty uses the published `qwen3-tts-native` release for this OS/arch; override for custom builds |
| `qwen_tts_native_sha256` | platform release | `QWEN_TTS_NATIVE_SHA256` / `OBEY_VOICE_QWEN_NATIVE_SHA256` | Archive digest matching the default or configured URL |
| `qwen_tts_stream` | `auto` | `QWEN_TTS_STREAM` | Whether Qwen's PCM chunks are played as they arrive instead of after the whole sentence. `auto` streams only where it was measured not to break up: `tts_place: lan`, or a local package whose `backend_hint` is `cuda`. `on` / `off` force it. A worker that cannot stream ignores all three. See [Chunk playback](#chunk-playback-qwen-stage-b) |
| `qwen_tts_timeout` | `120` | `QWEN_TTS_TIMEOUT` | Per-request native/external worker timeout in seconds |
| `qwen_tts_mode` | empty | `QWEN_TTS_MODE` | Product setup resolves empty to `customvoice` |
| `qwen_tts_voice` | empty | `QWEN_TTS_VOICE` | CustomVoice preset (`Vivian`, `Serena`, …) or shipped clone (`Veronica`). Empty resolves to `Veronica`. **A clone voice cannot travel to a speech worker** — its throat is a recording on this computer — so under `tts_place: lan` that computer speaks in its own voice and Obey Voice warns at conversation start and in `doctor`. Pick a preset voice for LAN speech |
| `qwen_tts_language` | empty | `QWEN_TTS_LANGUAGE` | Synthesis language; setup resolves empty to `Auto` |
| `qwen_tts_instruction` | empty | `QWEN_TTS_INSTRUCTION` | Reserved for an installable instruction-capable Qwen model tier |
| `qwen_tts_reference_audio` | empty | `QWEN_TTS_REFERENCE_AUDIO` | Authorized local reference WAV for an approved clone workflow |
| `qwen_tts_reference_text` | empty | `QWEN_TTS_REFERENCE_TEXT` | Transcript required by the approved clone workflow |
| `qwen_tts_consent` | `false` | `QWEN_TTS_CONSENT` | Explicit consent/authorization gate for reference voice use |
| `output_device` | empty | `OUTPUT_DEVICE` | Playback device name; empty follows the system default |
| `stt_provider` | `sherpa-parakeet` | `STT_PROVIDER` | STT backend: `sherpa-parakeet` (NVIDIA Parakeet TDT — default; the best in-process option, and the most accurate), `sherpa` (Whisper), `sherpa-streaming`, `sherpa-offline`, `whispercpp`, or `transcribecpp` (optional; see below) |
| `input_device` | empty | `INPUT_DEVICE` | Capture device name; empty follows the system default |
| `stt_mode` | empty | `STT_MODE` | STT mode for the preferred provider+mode schema: `offline` or `streaming` for `sherpa`, `offline` for `parakeet`, `cli` for `whispercpp`, `offline` for `transcribecpp` |
| `sherpa_streaming_model` | `en-2023-06-26` | `SHERPA_STREAMING_MODEL` | sherpa-onnx streaming model |
| `whisper_model` | `small` | `WHISPER_MODEL` | sherpa-onnx Whisper model size |
| `parakeet_model` | `tdt-0.6b-v2` | `PARAKEET_MODEL` | Parakeet model for `sherpa-parakeet`: `tdt-0.6b-v2` (English), `tdt-0.6b-v3` (25 languages), `tdt-110m` (small, fast) |
| `whisper_quantized` | `true` | | Prefer quantized Whisper models |
| `whispercpp_binary` | `whisper-cli` | `WHISPERCPP_BINARY` | whisper.cpp CLI executable |
| `whispercpp_model` | `base.en` | `WHISPERCPP_MODEL` | Downloadable whisper.cpp model name |
| `whispercpp_model_path` | `~/.cache/festival-voice/models/whispercpp/ggml-base.en.bin` | `WHISPERCPP_MODEL_PATH` | whisper.cpp model path |
| `transcribecpp_binary` | `transcribe-cli` | `TRANSCRIBECPP_BINARY` | transcribe.cpp CLI executable. A path is used as given; a bare name is looked up on `PATH`, then in the copy `models ensure` manages |
| `transcribecpp_model` | `parakeet-tdt-0.6b-v2` | `TRANSCRIBECPP_MODEL` | GGUF the `transcribecpp` provider loads. This is upstream's full model slug, so it is spelled longer than `parakeet_model`'s `tdt-0.6b-v2` — not a typo |
| `vad_enabled` | `true` | | Enable voice activity detection |
| `vad_silence_duration` | `1.2` | | Seconds of silence before ending speech (raise to stop being cut off) |
| `vad_threshold` | `0.6` | `VAD_THRESHOLD` | Speech-detection confidence (raise to ignore background noise) |
| `vad_min_speech_duration` | `0.25` | `VAD_MIN_SPEECH_DURATION` | Minimum speech length in seconds (raise to ignore brief noises) |
| `voice_frontend_enabled` | `false` | `VOICE_FRONTEND_ENABLED` | Local AEC/NS/AGC on mic input (off by default: the noise suppressor currently over-suppresses normal-volume speech; enable only with barge-in) |
| `tui_mouse_enabled` | `false` | `TUI_MOUSE_ENABLED` | Claim the mouse so the wheel/trackpad is routed to the transcript viewport. Off by default so the terminal keeps click-drag selection and copy; turning it on means selecting needs a modifier (option/fn/shift). Either way `Page Up`/`Page Down` scroll, and with it off the wheel scrolls whenever the composer is empty. Applies on next enter/leave of conversation (Settings → Tools toggle or re-entry); no full process restart required for the TUI claim |
| `agent_name` | `Obey` | | Display name |
| `persona` | `obey` | `PERSONA` | Prompt document name for the interactive persona |
| `prompts_dir` | empty | `PROMPTS_DIR` | Prompt document directory; defaults to `~/.obey/agents/voice/festival-voice/prompts` when unset |
| `skills_enabled` | `true` | `SKILLS_ENABLED` | Agent Skills (`SKILL.md`) discovery for Ollama. Default on; set `false` to disable. Discovers project/workspace/user skill frontmatter into the system prompt; not a pre-activation sandbox. Claude/Grok already discover skills via their CLIs. |
| `skills_dir` | empty | `SKILLS_DIR` | Extra Obey Voice skills root (after project/workspace/user harness dirs); defaults to `~/.obey/agents/voice/festival-voice/skills` when unset. |
| `work_dir` | empty | `OBEY_VOICE_WORK_DIR` | Directory the agent's file and shell tools start in. **A default, not a sandbox** — a tool may still reach an absolute path outside it. Empty uses the directory Obey Voice was launched from; the binary's own folder, `/`, and any path that is not an existing directory fall back to your home folder. `obey-voice serve --work-dir DIR` overrides it for one process, and an override that is not an existing directory is a startup error rather than a silent fallback. The bare `WORK_DIR` is still honored for compatibility but `OBEY_VOICE_WORK_DIR` wins when both are set — see [Where the working directory came from](#where-the-working-directory-came-from). |
| `models_dir` | `~/.cache/festival-voice/models` | `MODELS_DIR` | Model download directory |
| `language` | `en-US` | | Recognition language |
| `max_history` | `10` | | Saved conversation history length |
| `listen_timeout` | `10` | | Listen timeout in seconds |
| `phrase_time_limit` | `30` | | Maximum phrase length in seconds |

## Where the working directory came from

The agent's working directory decides where every file tool, shell tool and
`camp` call lands — and therefore which campaign workspace it acts on. It is
resolved in one order, and the layer that won is reported everywhere the
directory is:

1. `--work-dir DIR` (serve; a path that does not exist is a startup error)
2. `OBEY_VOICE_WORK_DIR`, else the legacy bare `WORK_DIR` (both reach the same
   setting; the prefixed name wins when both are set)
3. `work_dir` in config.yaml
4. the directory Obey Voice was launched from
5. your home directory, then the temp directory, when none of the above is a
   usable directory

**The environment outranks config.yaml.** Values are layered env-over-file, so
pinning `work_dir` in config.yaml does *not* protect you from a stray
`WORK_DIR` — with `work_dir: /tmp/A` in the file and `WORK_DIR=/tmp/B`
exported, the agent works in `/tmp/B`. That is why the source is reported
rather than assumed:

```
$ WORK_DIR=/tmp/B obey-voice paths
  Work dir           /tmp/B
  Work dir source    env (the WORK_DIR environment variable)
```

Steps 2 and 5 are the silent ones: config.yaml shows nothing, and a generic
`WORK_DIR` exported by a shell profile or an `.envrc` for its own purposes
moves the agent for every launch. So they are reported by name:

```bash
obey-voice paths          # Work dir source + Campaign rows (--json: work_dir_source, campaign)
obey-voice doctor         # work-dir-source: WARN for an env var or a fallback,
                          #   naming the variable — including the one whose bad
                          #   path caused the fallback
obey-voice config get work_dir --json   # source: env when a bound variable decides it
obey-voice serve          # Work dir / Campaign lines on the banner (--banner-json: same fields)
```

The campaign is detected by walking up from the working directory to the first
`.campaign/campaign.yaml` and reading its `name` and `id`. Nothing is executed
to learn it — the file read is the detection — and the same line is injected
into the model's grounding block, so the agent states which workspace it is in
rather than assuming:

```
- Working directory: /Users/you/Dev/My_Tools
- Campaign: My_Tools (root: /Users/you/Dev/My_Tools)
```

## Chunk playback (Qwen Stage B)

A Stage B `qwen3-tts-worker` emits PCM while it is still synthesizing, one
numbered `pcm_meta` chunk at a time, instead of one blob at the end. When Obey
Voice plays those chunks as they arrive, a reply starts speaking on the first
chunk rather than on the finished sentence.

That is only safe where synthesis runs **faster than realtime**. Streaming
changes when the first sample arrives; it does not change how fast samples are
produced. If the engine produces audio more slowly than the speaker consumes
it, the player drains the buffer and the sentence breaks up mid-word — which is
worse than waiting, because a whole-utterance segment never runs dry.

Measured 2026-08-27 (`qwen3-tts-native` `docs/latency/stage_b_streaming_2026-08-27.json`,
0.6b, multi-sentence fixture, 15 runs each):

| Backend | TTFA p50 | RTF | Underruns | Gate |
|---------|----------|-----|-----------|------|
| CUDA (RTX 5060 Ti) | 435 ms | 0.55 | 0 | pass |
| Metal (M4 Max) | 1295 ms | 1.66 | 120 | **fail** |

Metal's RTF gets *worse* when streaming (1.40 whole-utterance → 1.66), and the
pre-buffer that would prevent its dropouts is 15.6 s on a 23 s reply — so the
effective time to first audio would be ~16 s, not the 1.3 s the TTFA column
suggests. `auto` therefore streams over `tts_place: lan` (the CUDA offload
target this exists for) and on a local package whose install manifest reports
`backend_hint: cuda`, and keeps whole sentences everywhere else, including any
backend nobody has measured.

`doctor` prints the decision as the `qwen3-tts-streaming` row. If a reply does
break up, the turn's metrics count it (`Underruns`, per segment and per turn)
and the session says so once — set `qwen_tts_stream: off` to go back to whole
sentences.

Two things worth knowing about a **local CUDA** package: Obey Voice does not
set `QWEN3_TTS_BACKEND`, and the worker's own auto backend selection does not
always reach the fast all-CUDA placement (~3.5× slower when it misses; see
`docs/benchmarks/F8-qwen-tts-cuda-2026-08-27.json`). The worker inherits the
environment Obey Voice was started in, so exporting `QWEN3_TTS_BACKEND=cuda`
before launching is what makes the measured numbers the ones you get.

### Measuring it here (E9 follow-up)

The numbers above are the worker's, measured in `qwen3-tts-native`'s own
harness. The product number is the full turn, and it is measured with the
existing bench, not a new one: run `just bench` against a conversation fixture
twice on the same machine and the same worker — once with `qwen_tts_stream:
off` and once with `on` — and compare `PlaybackStartElapsed`, which is the
listener's actual wait, plus per-segment `RealtimeFactor` and `Underruns` from
the turn metrics. A `PlaybackStartElapsed` that does not move means STT and the
brain dominate and the TTS win is invisible; any non-zero `Underruns` on the
streamed run means that machine must not stream, whatever its TTFA says. The
two-machine Topology B run (host here, CUDA worker over `speech-serve`) is the
one that exercises the LAN default and needs both computers.

## transcribe.cpp (optional STT)

`stt_provider: transcribecpp` runs the same NVIDIA Parakeet TDT 0.6B v2 weights as
the default, but as a GGUF through the external [transcribe.cpp](https://github.com/handy-computer/transcribe.cpp)
CLI, which puts them on a GPU (Metal or CUDA) instead of CPU ONNX. It is opt-in: the
default stays `sherpa-parakeet`, and nothing downloads or checks the GGUF until
you select the provider. Utterance-final only — there is no streaming mode.

**It requires a GPU and refuses to run without one.** Measured on the same
weights, transcribe.cpp on Metal decodes 3.1x faster than the default, but on CPU
it is *slower* than the default. `--backend auto` would fall back to CPU silently,
so obey-voice probes `transcribe-cli --list-devices` at startup and fails with an
error naming the remedy if no GPU lane is listed. On a CPU-only machine, stay on
`sherpa-parakeet`.

Two things are needed: the `transcribe-cli` executable and the GGUF. On Apple
Silicon `obey-voice models ensure` installs both — it fetches a pinned
transcribe.cpp **0.2.2** CLI build alongside the weights, so there is nothing to
compile. Upstream publishes no Homebrew formula and its release tarballs carry
only the shared libraries (the CLI is a `cmake` `examples/` target), which is why
the build is vendored rather than pointed at.

On every other platform the CLI is not bundled yet. Build
[transcribe.cpp](https://github.com/handy-computer/transcribe.cpp) v0.2.2 from
source and set `transcribecpp_binary` to the resulting `transcribe-cli`;
`models ensure` still fetches the GGUF. `doctor` says which of the two cases you
are in.

The binary is resolved in that order of authority: an explicit
`transcribecpp_binary` path wins, then `transcribe-cli` on `PATH`, then the
managed copy under `<models_dir>/transcribecpp/bin/`. Managed comes last, so
installing your own CLI keeps overriding the vendored one. The binary, the GGUF
and the GPU lane are all checked at startup and by `obey-voice doctor`, which
reports them as `transcribecpp-binary`.

The GGUF weights are Parakeet TDT 0.6B v2, licensed **CC BY 4.0** and
redistributed as GGUF by
[handy-computer/parakeet-tdt-0.6b-v2-gguf](https://huggingface.co/handy-computer/parakeet-tdt-0.6b-v2-gguf);
the transcribe.cpp runtime and its vendored CLI build are **MIT**. `models
ensure` and `models status` print both attributions alongside the assets. The
licenses cover what you download, not this project.

Each utterance is one short-lived CLI process with its own model load, so the
per-utterance cost includes loading the GGUF — the win measured in the A/B is
compute, not end-to-end warm latency.

## Sortformer meeting diarizer (optional)

`speaker.meeting.diarizer` picks the engine behind the **offline meeting review
pass** — the timeline meeting speaker labels are read from.

| Value | What runs |
|---|---|
| `sherpa` (default) | Managed pyannote segmentation 3.0 + NeMo TitaNet, in process. Every install gets this |
| `sortformer` | NVIDIA Streaming Sortformer through the same transcribe.cpp CLI as above (`transcribecpp_binary`), plus a managed 139 MB GGUF |

It is opt-in, and independent of `stt_provider`: either engine pairs with any
recognizer, and unlike the STT provider it does **not** require a GPU — Sortformer
on CPU still beats the sherpa review path it replaces. Live labels, embeddings
and enrolled names stay on the sherpa path either way.

`models ensure` plans that GGUF only when all three of these hold: `speaker.enabled`
is on, meeting diarization is on (`speaker.meeting.enabled`, **Settings → Meeting →
Speaker diarization**), and this key is `sortformer`. With any of them off nothing
downloads and nothing is checked — doctor and the settings row stay quiet about
weights that the plan would not fetch. (Weights already downloaded keep their claim
even when meetings are switched off, so `models clean` will not offer to delete
them.)

Set it in **Settings → Meeting → Diarization engine**, or in the config file. An
unrecognised value is a config error, not a silent fall back. `obey-voice doctor`
reports the CLI and the weights as `sortformer-diarizer`, and adds a
`sortformer-expected-speakers` warning when a count is configured that this
engine cannot use: Sortformer has no speaker-count input and resolves up to four
speakers on its own, so `speaker.meeting.num_speakers` has no effect while it is
selected (switch back to `sherpa` and the count applies again).

What it changes about a meeting — and what it deliberately does not — is in
[meetings.md](meetings.md). The weights are under the **NVIDIA Open Model
License** (the runtime and CLI stay MIT), and `models ensure` / `models status`
print that attribution with the asset. Measured against the sherpa path in
[`benchmarks/T3-diar-ab-sortformer-2026-08-26.json`](benchmarks/T3-diar-ab-sortformer-2026-08-26.json).

## Agent Skills (Ollama)

With `skills_enabled=true` (the product default unless explicitly disabled), the
Ollama provider discovers Agent Skills via the
cross-client **`.agents/skills`** convention
([agentskills.io](https://agentskills.io/client-implementation/adding-skills-support))
— **project/workspace then user** — plus Obey Voice's own skills root:

1. `<cwd>/.agents/skills/*/SKILL.md` — project skills (shared with Codex, VS Code, camp, …)
2. `<nearest ancestor>/.agents/skills/*/SKILL.md` — workspace/project-root skills
3. `~/.agents/skills/*/SKILL.md` — user skills
4. `skills_dir` (default `~/.obey/agents/voice/festival-voice/skills`) — Obey Voice-only

Ollama does **not** scan `.claude/skills`; the Claude Code provider owns that
path, and dual-scanning would duplicate skills when both trees are projected.
Duplicate skill names resolve with **project first**. Ollama advertises each
skill's name and description in the system prompt and offers a `read_skill` tool
to load full instructions on demand (progressive disclosure).

Obey Voice also performs harness-side semantic activation. At startup it batches
each skill's name and description through `ollama_embedding_model` and caches
the vectors. Before every turn it embeds the user prompt, selects the closest
skills above the relevance threshold, and injects only those full `SKILL.md`
bodies into an `<activated_skills>` block. This keeps Tier 1 discovery compact
while making Tier 2 activation reliable for local models. Pull the default
embedding model once with `ollama pull nomic-embed-text`. If the embedding model
is unavailable, Obey Voice logs a warning and falls back to the catalog plus
model-driven or explicit `read_skill`; the conversation still runs.

Optional frontmatter `allowed-tools` (Agent Skills experimental field) is
retained as catalog metadata but does not restrict Obey Voice's Ollama runtime.
Skills add instructions rather than capabilities, all implemented tools remain
available after a skill is loaded, and the model may load multiple skills in one
turn. The global safety gates still apply: `voice_tools_enabled` /
`remote_tools_enabled` must allow tools before any tool call can run.

Claude and Grok pick up skills via their own CLIs. Remote `obey-voice serve` still
gates all tools (including `read_skill`) behind `remote_tools_enabled`.

The TUI Settings screen exposes local tools under **Tools**, and Agent Skills
when the brain provider is Ollama. Changes are persisted immediately and take
effect when the conversation runtime is re-entered or restarted.

```text
# project (cwd where samantha was started)
./.agents/skills/hello/SKILL.md

# workspace/project root (nearest ancestor)
<workspace>/.agents/skills/campaign-context/SKILL.md

# user
~/.agents/skills/hello/SKILL.md

# Obey Voice config root
~/.obey/agents/voice/festival-voice/skills/hello/SKILL.md
```

```markdown
---
name: hello
description: Greet the user warmly.
---

# Hello skill
Say hello to the user.
```

## STT schema migration

The preferred STT schema is `stt_provider` + `stt_mode` (e.g. `stt_provider: sherpa` with `stt_mode: streaming`). The legacy compound aliases (`sherpa-streaming`, `sherpa-offline`) still work with `stt_mode` unset and are never rewritten; combining a compound alias with a conflicting `stt_mode` is a config error.

**Settings → STT** writes this schema directly: picking a recognizer stores the
canonical `stt_provider` plus its `stt_mode`, never a compound alias, and keeps
the mode you already had when the recognizer still supports it. A change takes
effect on the next conversation — the running one keeps the recognizer it
started with.

Use `obey-voice config migrate --dry-run` to preview the explicit
`stt_provider`/`stt_mode` values that would preserve the current STT behavior.
Dry runs report the config path and proposed values without writing files. Use
`obey-voice config migrate --write` to apply the migration; it creates a
timestamped `.bak` file before replacing an existing config. The write path
updates YAML via `yaml.v3`, so comments and unrelated keys are preserved where
possible, but scalar formatting around touched keys may be normalized.
