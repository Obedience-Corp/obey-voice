# Configuration

[Back to README](../README.md)

Config lives at `~/.obey/agents/voice/obey-voice/config.yaml`. Values can also be overridden with environment variables where listed.

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

Persona **profiles** live under `~/.obey/agents/voice/obey-voice/personas/<id>/persona.yaml`.
On load, the active profile overlays `agent_name`, the persona prompt name, and per-persona TTS
(`tts.provider` + `tts.voice` → `tts_provider` and either `tts_voice` or `qwen_tts_voice`).
Each persona can use any supported TTS backend and any voice for that backend.
Prompt bodies stay in `prompts/` (see `obey-voice prompts`).

Shipped personas live in the catalog (`internal/persona/shipped/*.yaml` plus a
matching `pkg/voiceagent/prompts/defaults/persona.<id>.yaml`, with Obey's prompt
in `persona.yaml`). Adding a companion requires those two files.
**obey** is the startup persona on a fresh install. All shipped profiles inherit
the application brain and pin distinct voices: Obey / Phonon Harper,
Samantha / Phonon Juniper, Jarvis / Phonon Edwin, Veronica / Phonon Zoey,
Dylan / Phonon Fennick the Young Elf, and Santa / Kokoro `am_santa`.
**Obey (default)** loads the complete saved Obey profile; unset fields inherit
Settings. Existing profiles are preserved;
see [shipped personas](personas.md#shipped-personas) to adopt the new voices.

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
| `active_persona` | `obey` | `ACTIVE_PERSONA` | Default persona for new conversations: the profile id under `personas/<id>/` a start uses when none is chosen. Set by `persona use`; it does not say which persona a running conversation is speaking as. |
| `agent_name` | `Obey` | | Display name (overlaid from active persona) |
| `user_display_name` | empty | | Your name in prompts and chat labels. Empty keeps `speaker-1` and `{user_name}` as the OS login |
| `persona` | `obey` | `PERSONA` | Prompt document name for kind=persona (overlaid from active persona) |
| `compact_prompt` | *(embedded default)* | `COMPACT_PROMPT` | Prompt document name for kind=compact — the `/compact` summarize-turn instruction |
| `brain_provider` | `ollama` | `BRAIN_PROVIDER` | Brain backend: `ollama`, `claude`, `cursor`, or `grok` |
| `ollama_model` | empty | `OLLAMA_MODEL` | Ollama model name. Live worker routing requires a model with tool support; a model that rejects routing tools reports a compatibility error instead of silently answering without them. |
| `cursor_model` | empty | `CURSOR_MODEL` | Model `cursor-agent` is asked for. Empty uses the CLI's own default. |
| `grok_model` | empty | `GROK_MODEL` | Model the Grok CLI is asked for. Empty uses the CLI's own default. |
| `claude_model` | empty | `CLAUDE_MODEL` | Model the Claude CLI is asked for (`--model`), such as `sonnet`, `opus` or `haiku`. Empty uses the CLI's own default. A persona's brain model on Claude lands here. Settings → Model and the persona form list the models the installed `claude` CLI reports for its version, account and settings (it is asked without a prompt, so no model is called), plus this value when the CLI does not list it. Settings uses the list read when the app starts; the persona form asks again each time it opens. A CLI too old to list models, or one that does not answer within 3 seconds, offers only `default` and this value. |
| `ollama_embedding_model` | `nomic-embed-text` | `OLLAMA_EMBEDDING_MODEL` | Ollama embedding model used to match each user prompt to relevant Agent Skills. Set empty to disable semantic routing and retain model-driven `read_skill` fallback. |
| `skills_similarity_threshold` | `0.55` | `SKILLS_SIMILARITY_THRESHOLD` | Minimum cosine similarity for skill candidates; applicability is confirmed before activation. Tune when using an embedding model with a different score distribution. |
| `ollama_host` | `http://localhost:11434` | `OLLAMA_HOST` | Ollama server URL |
| `ollama_num_ctx` | `32768` | `OLLAMA_NUM_CTX` | Talk conversation window. 32k is a real session; 8k is the old lab cap. `0` uses the server model default, which silently truncates long prompts from the top (system prompt first). |
| `voice_worker_num_ctx` | `0` | `VOICE_WORKER_NUM_CTX` | Ollama worker window: `0` uses the selected worker model's advertised capacity (128k when metadata is absent). A positive token count overrides it independently of the voice window. Workers automatically compact at `continuity_soft_limit_ratio` (default 80%). |
| `ollama_num_batch` | `128` | `OLLAMA_NUM_BATCH` | How many prompt tokens Ollama re-reads per pass. Unset, Ollama picks it mainly from the context window (512 at or below 4096, 1024 above, 2048 above 32768), downgrading toward 512 if the model's predicted VRAM will not fit — it weighs the model's memory footprint but not its architecture. Values above `ollama_num_ctx` are clamped to it at load. This is a prefix-**reuse** control, not a speed one: llama.cpp keeps its last reusable checkpoint about one batch back from the end of the prompt, so a smaller batch skips more of an unchanged conversation (14% skipped at 1024 vs 89% at 128 on `qwen3.8-uncensored:27b`, 4.4 s to 0.8 s) while reading brand-new prompts slightly slower. `0` restores Ollama's choice. See `docs/benchmarks/B2-qwen-ttft-mechanism-2026-08-28.md`. |
| `ollama_keep_alive` | `10m` | `OLLAMA_KEEP_ALIVE` | How long Ollama keeps the model resident between turns (Go duration; empty = server default). |
| `ollama_think` | `auto` | `OLLAMA_THINK` | Model thinking: `auto` and `false` disable it; `true` enables it. `low`/`medium`/`high`/`max` depend on model support. Available in Settings → Brain → Thinking; applies to the next conversation. Ctrl+K only controls visibility. |
| `claude_max_session_tokens` | `0` | `CLAUDE_MAX_SESSION_TOKENS` | Opt-in fuse: caps the prompt the `claude` CLI replays on `--resume`. Past it the brain starts a fresh CLI session from recent history. `0` (default) trusts the CLI session and resumes forever — recommended for interactive use; set a cap for unattended/serve setups. |
| `claude_session_warn_tokens` | `60000` | `CLAUDE_SESSION_WARN_TOKENS` | Replayed-prompt size at which a visible warning appears (log + activity feed), once per CLI session. Nothing is dropped. `0` disables the warning. |
| `voice_tools_enabled` | `true` | `VOICE_TOOLS_ENABLED` | Local tools (`list_files` / `read_file` / `write_file` / `run_command` / `web_search` / `fetch_url` for Ollama). Default on; set `false` to disable. Switching brain provider to Ollama also re-enables when the key was dumped false. Remote `obey-voice serve` still uses `remote_tools_enabled` (default off). |
| `tool_command_timeout` | `30` (clamped 1–120) | `TOOL_COMMAND_TIMEOUT` | Maximum seconds for one local `run_command` invocation. The whole brain turn has its own timeout. |
| `voice_work_enabled` | `false` | `VOICE_WORK_ENABLED` | Opt into the experimental conversation work inbox. Saves pending background results with the conversation; paid tiers require explicit advisory uncapped consent in Workers Settings; story-writing narration is unavailable. Restored updates remain held until explicitly resumed. Reading existing files aloud remains available. |
| `voice_work_narration` | `status_only` | `VOICE_WORK_NARRATION` | How much a spawned worker narrates itself. `status_only` stays quiet until you ask what's happening; `milestones` speaks one short line each time the worker moves from one kind of work to another (a closed fold row), gated on the mic being idle. Never one line per tool call. See [delegation](delegation.md#what-the-worker-is-doing). |
| `voice_worker_max_concurrent` | `1` (1–8) | `VOICE_WORKER_MAX_CONCURRENT` | **Legacy mode only.** How many spawned jobs run at the same time; the rest wait in order. Tiered Workers Settings use `voice_workers.policy.concurrent_workers` instead. |
| `voice_worker_job_budget` | `0` (0–86400) | `VOICE_WORKER_JOB_BUDGET` | **Legacy mode only.** Optional limit, in seconds, on how long a spawned job may run before it is stopped and you are told once. The default is no limit: a job runs until it finishes or you cancel it. Tiered Workers Settings use each tier's `runtime_seconds` instead. Zero means no limit. See [delegation](delegation.md#what-the-worker-is-doing). |
| `voice_worker_boundary` | `true` | `VOICE_WORKER_BOUNDARY` | Keeps a spawned worker inside the directory it was launched in, or that directory's campaign root when it is inside one: its prompt says so, and the Ollama worker's file tools and shell refuse paths outside it. No campaign is needed. See [delegation](delegation.md#where-a-worker-may-go). |
| `remote_tools_enabled` | `false` | | Allow network-triggered turns from `obey-voice serve` to invoke tools; keep off unless remote clients are trusted. |
| `calibre_enabled` | `false` | `CALIBRE_ENABLED` | Opt in to Calibre library browse/search, TUI Library + picker, and `--from-library` |
| `calibre_library_path` | empty | `CALIBRE_LIBRARY_PATH` | Calibre library path (empty uses Calibre's default library) |
| `calibredb_binary` | empty | `CALIBREDB_BINARY` | `calibredb` path; empty uses PATH then macOS app bundle / `/opt/calibre` |
| `calibre_convert_binary` | empty | `CALIBRE_CONVERT_BINARY` | `ebook-convert` path used to convert MOBI/AZW-family books to EPUB |
| `calibre_prefer_format` | `epub` | `CALIBRE_PREFER_FORMAT` | Preferred book format when resolving (`epub`, `pdf`, then convertible MOBI/AZW-family formats) |
| `tts_provider` | `kokoro` | `TTS_PROVIDER` | TTS backend: `kokoro`, `qwen3-tts`, `audio8` (experimental quality preview; needs the Audio8 export under `models_dir/audio8-tts`), or `openai-speech` (any OpenAI-compatible `/v1/audio/speech` server; Phonon via Ollama by default, see [OpenAI-compatible speech](openai-speech.md)) |
| `openai_speech_runtime` | `auto` | `OPENAI_SPEECH_RUNTIME` | `auto` uses managed native Phonon for an unconfigured Linux endpoint and Ollama on macOS; `native` owns a local server; `external` preserves the configured server |
| `openai_speech_backend` | `auto` | `OPENAI_SPEECH_BACKEND` | Native Phonon device: `auto`, `cpu`, `vulkan`, `cuda`, or `metal`; startup checks the actual device |
| `openai_speech_native_binary` | empty | `OPENAI_SPEECH_NATIVE_BINARY` | Optional absolute path to a compatible native server; otherwise setup builds pinned source |
| `openai_speech_url` | `http://localhost:11434/v1` | `OPENAI_SPEECH_URL` | External speech API base URL; explicit URLs/models prevent automatic native selection |
| `openai_speech_model` | `phonon` | `OPENAI_SPEECH_MODEL` | External server's model ID (`phonon` on Ollama). The managed native runtime uses pinned `gradium/phonon-31m-en` |
| `openai_speech_voice` | `default` | `OPENAI_SPEECH_VOICE` | Voice name on that server. Phonon on Ollama 0.40.11+ offers named voices after setup; `default` remains available. Settings → Voice and the Personas form list the server's catalog when it publishes one (see [Voices and personas](openai-speech.md#voices-and-personas)); a name it does not list can still be set here and stays listed in both. `obey-voice voices` lists what the server has. Persona-overridable |
| `openai_speech_api_key` | empty | `OPENAI_SPEECH_API_KEY` | Bearer token for a speech API that needs one. Ollama does not |
| `openai_speech_timeout` | `60` | `OPENAI_SPEECH_TIMEOUT` | Seconds one speech request may take before it is abandoned |
| `voice_fallback_provider` | `kokoro` | `VOICE_FALLBACK_PROVIDER` | One-sentence runtime fallback after the selected provider fails; set empty/disabled to turn it off |
| `tts_voice` | `af_heart` | `TTS_VOICE` | Kokoro voice name |
| `audio8_voice` | empty | `AUDIO8_VOICE` | Which Audio8 voice speaks. Empty uses the reference packaged with the export; otherwise a voice registered with `obey-voice voices register` (see [Audio8 voices](audio8-voices.md)). Not an enum: registered names are yours, so an unknown one is reported by the provider rather than by `config set`. Switching voices re-pays the ~1.0 s prefix prime |
| `tts_place` | `local` | | Where Qwen speech runs: `local` (this computer) or `lan` (a `speech-serve` host — see [speech-serve.md](speech-serve.md)). Host topology, never a persona field |
| `tts_worker_url` | empty | | Speech worker address when `tts_place: lan`, e.g. `https://studio:7263`. `http://` only to loopback; never carries the token |
| `tts_worker_token` | empty | `OBEY_SPEECH_TOKEN` | Shared secret the speech worker requires. **Required** whenever `tts_place: lan` — a missing token is refused at validation, not at connect time. `config get` reports only whether it is set |
| `tts_lan_strict` | `false` | | Refuse to start a conversation when the speech worker is unreachable, instead of speaking with Kokoro here |
| `tts_lan_timeout_ms` | `200` | | Dial and handshake budget for the speech worker. It never bounds the worker's model load |
| `tts_lookahead` | `2` (1–8) | `TTS_LOOKAHEAD` | How many admitted speech segments a reply may hold ahead of the one playing. `2` is one segment playing while the next is synthesized, which is what the pipeline has always done. It is raised to the speech engine's own concurrency when that is higher; every shipped engine synthesizes one request at a time, so raising it alone buys nothing |
| `speech_speed` | `0.95` | | Playback speed |
| `playback_volume` | `1` | | Linear playback gain. `1` is unchanged; `0` is silent; values up to `4` boost a quiet speaker. Settings → Audio has a slider; takes effect immediately |
| `qwen_tts_binary` | empty | `QWEN_TTS_BINARY` | Empty uses the native package worker under `models_dir/qwen3-tts`; set with `qwen_tts_model` for an explicit external worker |
| `qwen_tts_model` | empty | `QWEN_TTS_MODEL` | Empty uses the native package model dir; otherwise an external worker model directory |
| `qwen_tts_model_tier` | `0.6b` | `QWEN_TTS_MODEL_TIER` | Preferred native tier (`0.6b` or `1.7b` when present) |
| `qwen_tts_native_url` | platform release | `QWEN_TTS_NATIVE_URL` / `OBEY_VOICE_QWEN_NATIVE_URL` | Empty uses the published `qwen3-tts-native` release for this OS/arch; override for custom builds |
| `qwen_tts_native_sha256` | platform release | `QWEN_TTS_NATIVE_SHA256` / `OBEY_VOICE_QWEN_NATIVE_SHA256` | Archive digest matching the default or configured URL |
| `qwen_tts_stream` | `auto` | `QWEN_TTS_STREAM` | Whether Qwen's PCM chunks are played as they arrive instead of after the whole sentence. `auto` streams only where it was measured not to break up: `tts_place: lan`, or a local package whose `backend_hint` is `cuda`. `on` / `off` force it. A worker that cannot stream ignores all three. See [Chunk playback](#chunk-playback-qwen-stage-b) |
| `qwen_tts_workers` | `1` (1–4) | `QWEN_TTS_WORKERS` | How many native Qwen worker subprocesses synthesize at once. Each one loads its own copy of the model at start, so this buys throughput with memory. Only the local native path honours it: a LAN worker's parallelism is the speech host's setting, and the managed Python path stays at one session. Above `1` it is an experiment — the default moves only when the run in [docs/benchmarks/README.md](benchmarks/README.md#qwen-worker-count-1-vs-2) clears its thresholds |
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
| `whispercpp_model_path` | `~/.cache/obey-voice/models/whispercpp/ggml-base.en.bin` | `WHISPERCPP_MODEL_PATH` | whisper.cpp model path |
| `transcribecpp_binary` | `transcribe-cli` | `TRANSCRIBECPP_BINARY` | transcribe.cpp CLI executable. A path is used as given; a bare name is looked up on `PATH`, then in the copy `models ensure` manages |
| `transcribecpp_model` | `parakeet-tdt-0.6b-v2` | `TRANSCRIBECPP_MODEL` | GGUF the `transcribecpp` provider loads. This is upstream's full model slug, so it is spelled longer than `parakeet_model`'s `tdt-0.6b-v2` — not a typo |
| `stt_custom_words` | *(empty)* | | Names and jargon the transcriber restores after decoding: `Samantha`; `Claude=clawed,cloud` names misheard forms that map to it exactly; `Ollama!` also catches near-misses when the word is rare (five or more letters and not an everyday word). Config file only, e.g. `["Samantha", "Claude=clawed,cloud", "Ollama!"]`. See [Custom words](#custom-words) |
| `stt_custom_words_from_personas` | `true` | | Adds every persona's display name to the custom words; rare names also catch near-misses |
| `stt_word_correction_threshold` | `0.12` | | Fuzzy acceptance score, 0.01–0.45; lower is stricter. Raise it only after `obey-voice stt audit` shows no wrong replacements |
| `stt_filler_removal` | `false` | | Drops um, uh and hmm from final transcripts before they are written down or answered. Answers such as `uh-huh` stay, and a lone `Hmm.` stays. See [Fillers and numbers](#fillers-and-numbers) |
| `stt_numbers` | `false` | | Writes spoken numbers as digits where a reader would: `thirty five` → 35, `five percent` → 5%, `twenty thousand dollars` → $20,000, `twenty twenty two` → 2022. Small counts stay words |
| `stt_postprocess_enabled` | `false` | | Runs a language model over each final transcript after the deterministic corrections. See [Post-processing model](#post-processing-model) |
| `stt_postprocess_provider` | `ollama` | | Provider for that model: any registered brain provider, independent of `brain_provider` |
| `stt_postprocess_model` | *(empty)* | | Model on that provider. Empty uses the provider's configured model (`ollama_model`, `grok_model`, `claude_model`, `cursor_model`); a small local model such as `qwen3.5:0.8b` answers fastest. With `stt_postprocess_provider: claude`, an empty value runs every final transcript on `claude_model`, so set this to pin a cheaper Claude model |
| `stt_postprocess_mode` | `patch` | | `patch` confirms near-miss names the custom words found, a few tokens per call and no call when there is nothing to ask; `rewrite` cleans the whole line under fidelity guards |
| `stt_postprocess_timeout_ms` | `1000` | | Deadline per call; on timeout the transcript goes on without the model and the timeout is counted |
| `stt_postprocess_keep_alive` | *(empty)* | | Ollama residency for this model, e.g. `10m` or `-1`; empty follows `ollama_keep_alive` |
| `stt_postprocess_candidate_threshold` | `0.45` | | Upper fuzzy score for a phrase to be offered to the model as a possible name in patch mode |
| `vad_enabled` | `true` | | Enable voice activity detection |
| `vad_silence_duration` | `1.2` | | Seconds of silence before ending speech (raise to stop being cut off) |
| `vad_threshold` | `0.6` | `VAD_THRESHOLD` | Speech-detection confidence (raise to ignore background noise) |
| `vad_min_speech_duration` | `0.1` | `VAD_MIN_SPEECH_DURATION` | Minimum speech length in seconds; keeps brief yes/no replies while audible-speech and noise checks still apply |
| `voice_frontend_enabled` | `false` | `VOICE_FRONTEND_ENABLED` | Local AEC/NS/AGC on mic input (off by default: the noise suppressor currently over-suppresses normal-volume speech; enable only with barge-in) |
| `talk_hotkey_enabled` | `true` | `TALK_HOTKEY_ENABLED` | A system-wide key that starts and ends one spoken turn from whatever app has focus. macOS (Carbon) and Windows (`RegisterHotKey`) register the combo themselves; everywhere else the conversation opens a private local endpoint and you bind your own shortcut tool to `obey-voice talk down` / `up` / `press`. The endpoint opens on every OS. `Ctrl+G` remains the mute |
| `talk_hotkey_mode` | `hold` | `TALK_HOTKEY_MODE` | How a press maps onto a turn: `hold` (press to talk, release to send), `toggle` (tap to start, tap to send), or `automatic` (a tap toggles, a press longer than 300 ms behaves as a hold) |
| `talk_hotkey` | *(empty — platform default)* | `TALK_HOTKEY` | The combo itself, e.g. `alt+space`, `ctrl+alt+t`, `f13`. Modifier names: `ctrl`/`control`, `alt`/`option`/`opt`, `shift`, `cmd`/`command`/`super` (glyphs work too). Needs at least one of ctrl, alt or cmd, or a bare function key — `shift` alone would swallow a typed character. Empty means the platform default: `ctrl+space` on macOS (the Obey Voice Mac app's key), `ctrl+alt+space` on Windows (where Ctrl+Space switches input methods and Alt+Space is the window menu). It is not used on Linux, where the key is whatever you bind to `obey-voice talk`. Also a row in Settings → Tools. A combo another app already owns is reported in the conversation, and `Ctrl+G` still works |
| `name_summon_enabled` | `false` | `NAME_SUMMON_ENABLED` | With the microphone open, only a finalized turn that **starts** with the agent's name is answered; everything else is dropped before the model sees it. The name is `agent_name`, its first word when the name has several (`Uncle Fu` also answers to `Uncle`), or any `name_summon_aliases` entry, matched case-insensitively and whole-word, optionally after one filler (`hey`, `ok`, `okay`, `hi`, `hello`, `yo`). The talk key always bypasses it and `Ctrl+G` mute keeps it deaf. Requires `voice_frontend` to be `nlms` or `webrtc`: with no echo cancellation the agent hears its own name in its own playback and summons itself, so `obey-voice doctor` reports an error, the Settings toggle refuses, and a conversation started that way runs without the gate. Applies to the next conversation |
| `name_summon_aliases` | *(empty)* | | Extra wake words beyond `agent_name` and its first word, e.g. `["computer"]`. Config file only — Settings has no row for a list. Same matching rules as the name itself |
| `tui_mouse_enabled` | `false` | `TUI_MOUSE_ENABLED` | Claim the mouse so the wheel/trackpad is routed to the transcript viewport. Off by default so the terminal keeps click-drag selection and copy; turning it on means selecting needs a modifier (option/fn/shift). Either way `Page Up`/`Page Down` scroll, and with it off the wheel scrolls whenever the composer is empty. Applies on next enter/leave of conversation (Settings → Tools toggle or re-entry); no full process restart required for the TUI claim |
| `agent_name` | `Obey` | | Display name |
| `persona` | `obey` | `PERSONA` | Prompt document name for the interactive persona |
| `prompts_dir` | empty | `PROMPTS_DIR` | Prompt document directory; defaults to `~/.obey/agents/voice/obey-voice/prompts` when unset |
| `skills_enabled` | `true` | `SKILLS_ENABLED` | Agent Skills (`SKILL.md`) discovery for Ollama. Default on; set `false` to disable. Discovers working-directory/project/workspace skill frontmatter into the system prompt; not a pre-activation sandbox. Claude/Grok already discover skills via their CLIs. |
| `skills_dir` | empty | `SKILLS_DIR` | Extra Obey Voice skills root (after working-directory/project/workspace dirs); defaults to `~/.obey/agents/voice/obey-voice/skills` when unset. |
| `work_dir` | empty | `OBEY_VOICE_WORK_DIR` | Directory the agent's file and shell tools start in. **A default, not a sandbox** — a tool may still reach an absolute path outside it. Empty uses the directory Obey Voice was launched from; the binary's own folder, `/`, and any path that is not an existing directory fall back to your home folder. `obey-voice serve --work-dir DIR` overrides it for one process, and an override that is not an existing directory is a startup error rather than a silent fallback. The bare `WORK_DIR` is still honored for compatibility but `OBEY_VOICE_WORK_DIR` wins when both are set — see [Where the working directory came from](#where-the-working-directory-came-from). |
| `models_dir` | `~/.cache/obey-voice/models` | `MODELS_DIR` | Model download directory |
| `language` | `en-US` | | Recognition language |
| `max_history` | `40` | | Saved conversation history length |
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

## Qwen persona voices

When creating a persona with `tts_provider: qwen3-tts`, an explicit
`qwen_tts_voice` is preserved. An unset Qwen voice inherits the provider default;
the Kokoro `tts_voice` value is not copied into the new profile. Older configs
that stored a recognized Qwen preset or shipped clone in `tts_voice` still copy
that selection.

Older versions could save a Qwen persona with a Kokoro voice such as `af_heart`
or `af_sky`. That saved voice overrides `qwen_tts_voice`, so changing the global
setting alone does not repair the persona. Existing profiles are kept intact.
To repair one, replace `my-persona` below with its ID and select a Qwen voice:

```bash
obey-voice persona edit my-persona --tts-provider qwen3-tts --voice Ryan
```

Alternatively, `obey-voice persona edit my-persona --voice ""` clears its saved
voice so it inherits `qwen_tts_voice` again. Ensure that global setting is empty
or a valid Qwen voice, then start a new conversation.

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

## Custom words

Speech-to-text spells names and jargon the way its training data did: a
persona called Veronica comes back as "veronika", Ollama as "olama", a product
name as two words. The correction runs after decoding, in the STT layer, on
every path that produces a final transcript — the conversation, meetings, the
remote pipeline and the benchmark — and it is deterministic: no model, no
network, well under a millisecond.

Terms come from `stt_custom_words` and, with `stt_custom_words_from_personas`
on (the default), from every persona's display name. Each term matches its own
spelling exactly however it was split, hyphenated or cased ("sam antha",
"obey voice"), and each alias maps a misheard form to it exactly:

```yaml
stt_custom_words:
  - Samantha
  - Claude=clawed,cloud
  - obey-voice
  - Ollama!
```

A trailing `!` opts a term into fuzzy matching, which also catches spellings
that were never listed. It is honored only for rare words — at least five
letters and not an everyday English word or common name — because replacing
real words is how a matcher turns "worked" into a product name. Persona names
get the same treatment automatically: Samantha and Veronica catch near-misses;
Santa and Joker match exactly. Fuzzy replacement never touches a span made only
of everyday words, and `stt_word_correction_threshold` (default `0.12`) sets
how close a near-miss must be.

Before trusting a term, replay it over what you have already said:

```bash
obey-voice stt audit                      # saved conversations and meeting bundles
obey-voice stt audit --terms 'Kokoro!'    # try an addition without editing config
obey-voice stt audit --threshold 0.2 --json
```

The audit lists every replacement it would make as phrase → term × count with
an example line, plus the near misses it would leave alone. A phrase that
should not change is a term to remove, to alias exactly, or to drop the `!`
from. The word list behind the rare-word rule is derived from SCOWL; its
notice ships with the binary's source under
`pkg/voiceagent/stt/correct/SCOWL-COPYRIGHT.txt`.

## Fillers and numbers

Two further stages run in the same place as custom words, each behind its own
key and off by default. They exist for meeting notes: the model you talk to
tolerates "um" and "thirty five" perfectly well, but a transcript you read back
does not.

`stt_filler_removal` drops hesitation sounds — um, uh, uhm, hmm and their
stretched spellings — and tidies what they leave behind: "Um, yeah, I think"
becomes "Yeah, I think", "and, uh, we'll go" becomes "and we'll go". A word
said three or more times in a row keeps one copy. Answers such as "uh-huh" and
"mm-hmm" stay, "oh" and "ah" stay because they carry meaning, and "like" is
never touched. A transcript is never emptied: a lone "Hmm." stays.

`stt_numbers` writes numbers the way a reader would and leaves the rest as
words. Compounds become digits ("thirty five" → 35), so do values of ten and
above, decimals ("three point five" → 3.5), years ("twenty twenty two" → 2022,
"nineteen ninety nine" → 1999), ordinals from the tenth ("twenty first" → 21st),
and any percentage or amount of money ("five percent" → 5%, "twenty thousand
dollars" → $20,000, "six million dollars" → $6 million). Small counts stay words
("two years", "one thing"), and a run that does not read as one number is left
alone ("one seventy to one ninety", "two three four", "a few hundred").

See what either stage would do before turning it on:

```bash
obey-voice stt audit --fillers --numbers          # over saved conversations and meetings
obey-voice stt preview --fillers --numbers --diff # the latest meeting, changed lines only
obey-voice stt preview --fillers --numbers        # the latest meeting, rendered as meeting.md
```

## Post-processing model

An optional language model runs after the deterministic corrections, off by
default. It has its own provider and model keys, so changing the conversation's
brain never changes it, and its own editable instruction documents. It fails
open: a timeout, a transport error or a reply that fails a guard keeps the
transcript as it was, and every such event is counted.

**Patch mode** (the default, for the talk loop) asks one question: of the
near-miss names the custom words stage found but did not apply, which did the
speaker mean? The model receives the transcript and the candidates, answers
with a JSON substitution list, and only a substitution whose span and name
were both offered is applied. When there are no candidates, which is nearly
every turn, the model is not called at all.

**Rewrite mode** (for stored transcripts) asks for a full cleanup: punctuation,
casing, mis-heard names, spoken numbers, fillers and self-corrections. Lines
are numbered; a reply that drops or merges lines keeps the whole window raw,
and a line is kept raw when it changes length by more than half, shares fewer
than half its words with the original, or gains more than one word that was
not spoken (digits and known names excepted): that is writing, not cleaning. Small
local models do less here than the deterministic stages: measured on real
meetings, a 3B model removed 11 of 26 fillers and converted 1 of 46 numbers
that `stt_filler_removal` and `stt_numbers` handle for free.

```yaml
stt_postprocess_enabled: true
stt_postprocess_provider: ollama
stt_postprocess_model: qwen3.5:0.8b   # empty means ollama_model
stt_postprocess_mode: patch
stt_postprocess_timeout_ms: 1000
```

If the model is not pulled, the stage does not run and `obey-voice stt audit
--model` says which `ollama pull` to run. Thinking is turned off for this
model's requests regardless of `ollama_think`, and Ollama is asked to load the
model as soon as the stage is built, off the critical path, so the first
transcript that needs it does not pay the load inside its deadline.

The instructions are prompt documents of kind `postprocess`, seeded to
`prompts/postprocess/patch.yaml` and `prompts/postprocess/rewrite.yaml` on
first start. Edit the file; the next turn uses it. `{known_names}` is filled
with the persona names and custom words, and the transcript is always the user
message, never part of the instruction, so a transcript cannot rewrite it.
`obey-voice prompts show postprocess` prints the one in use.

Try it before turning it on, on what you have already said:

```bash
obey-voice stt audit --model --sessions                          # patch over saved conversations
obey-voice stt audit --model --model-name qwen3:8b               # try another model on the provider
obey-voice stt preview --model --mode rewrite --diff              # rewrite the latest meeting, changed lines only
obey-voice stt audit --model --mode rewrite --sample 40           # rewrite 40 utterances, with the guard counts
```

Both commands report the model's counters: calls, turns skipped for want of
candidates, timeouts, substitutions applied and rejected, lines changed and
lines kept raw. Off the loop a rewrite run gets a 120 s deadline per window
unless `--timeout-ms` says otherwise. Read the counters before turning the
stage on: on one machine's saved transcripts an 8B general model confirmed two
of three false candidates in patch mode, and in rewrite mode fixed stutters and
casing the deterministic stages cannot, while inventing one sentence subject
the guard now catches.

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
— **working directory and project/workspace** — plus Obey Voice's own skills root:

1. `<cwd>/.agents/skills/*/SKILL.md` — project skills (shared with Codex, VS Code, camp, …)
2. `<nearest ancestor>/.agents/skills/*/SKILL.md` — workspace/project-root skills
3. `skills_dir` (default `~/.obey/agents/voice/obey-voice/skills`) — Obey Voice-only
4. Embedded `obey-voice` skill — app and persona configuration guidance

Shared global `~/.agents/skills` is not imported automatically. Ancestor
discovery stops before the home directory, and launching from home does not
activate its global skill collection. To deliberately include a shared folder,
set `skills_dir` to that folder. Individual project skill symlinks remain
supported.

The shipped skill is available without a checkout or a skill installation. It
explains the installed CLI's persona/config commands and requires reading back
saved changes. Filesystem skills named `obey-voice` override this fallback for
Ollama. Native-tool providers receive the shipped configuration instructions
directly in their app context. `skills_enabled=false` or disabling `obey-voice`
removes those detailed instructions; basic knowledge of the hosting app remains.
Ask the assistant to create a persona or change its voice; it can inspect the
CLI and perform requested changes through its worker/tools.

Ollama does **not** scan `.claude/skills`; the Claude Code provider owns that
path, and dual-scanning would duplicate skills when both trees are projected.
Duplicate skill names resolve with **project first**. Ollama advertises each
skill's name and description in the system prompt and offers a `read_skill` tool
to load full instructions on demand (progressive disclosure).

Obey Voice also performs harness-side skill selection. At startup it batches
skill names and descriptions through `ollama_embedding_model` and caches the
vectors. On a user turn, the closest matches above `skills_similarity_threshold`
are candidates, rather than instructions to activate them. A private, bounded
model check confirms that their procedures apply to the actual request before
full `SKILL.md` bodies enter the request's `<activated_skills>` block. Shared
words such as "web" do not turn a weather lookup into a website audit or a
Cloudflare setup task. Peer/group replies do not activate skills automatically.
The check leaves conversation history unchanged and uses the existing request
and output budget when one applies; it skips optional selection if doing so
would consume the task's last request or output allowance.

The selection instructions, activated-skill wrapper, and live/worker tool
guidance are editable system YAML documents named `skill-selection`,
`activated-skills`, `tool-surface-live` and `tool-surface-legacy` under
`<prompts_dir>/system/`. Inspect one with
`obey-voice prompts show system --name tool-surface-live`. Live Ollama conversations use `route` to delegate
current-information questions such as today's weather to a worker, which has
`web_search` and `fetch_url`; a dedicated weather tool is unnecessary.

Pull the default embedding model once with `ollama pull nomic-embed-text`.
If matching or confirmation is unavailable or invalid, automatic activation is
skipped. The catalog and model-driven/explicit `read_skill` remain available,
and the conversation continues.

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
# project (conversation working directory)
./.agents/skills/hello/SKILL.md

# workspace/project root (nearest ancestor)
<workspace>/.agents/skills/campaign-context/SKILL.md

# Obey Voice config root
~/.obey/agents/voice/obey-voice/skills/hello/SKILL.md
```

```markdown
---
name: hello
description: Greet the user warmly.
---

# Hello skill
Say hello to the user.
```

## Live routing (`voice_router_tools`)

With voice tools on, a live Ollama turn first decides whether the request is
conversation (`chat`), work for a worker (`spawn`) or reading aloud (`narrate`).
Other brains decide the same question with a private classifier, and this
setting does not change them.

| Value | What a live Ollama turn does |
| --- | --- |
| `route_only` (default) | Offers only the `route` tool. A chat decision is followed by a tools-free reply. |
| `json_route` (experimental) | Asks for the same route as a JSON object, with no tools, on the reply's own messages. |
| `legacy` | Offers the full worker tool list, for comparison. |

`json_route` is about prompt-cache reuse. Under `route_only` the route request
carries the route tool schema and the reply does not. Ollama renders tool schemas
into the prompt next to the system text, so the reply reads the conversation
history again. Under `json_route` both requests carry the same system message,
history and newest user message, no tools, and the same model, thinking setting
and runner options. The route request puts its instruction after the newest user
message and constrains the answer with the route schema; the reply adds only a
one-line "routing is complete, answer now" instruction at that same point. Whether
Ollama actually reuses the cached prefix depends on the model's chat template
and runner, and has not been measured on real models yet.

Everything else matches `route_only`:

- `spawn` and `narrate` dispatch exactly as with the route tool, including the
  worker effort tier when tiered workers are on.
- The decision is private. Neither the JSON nor the exchange is spoken or kept in
  history. The turn shows it as a `route` tool event with the outcome and how
  long the decision took.
- A reply that is not a valid route object counts as a missing route: one retry
  through the existing recovery request, then the turn fails with the existing
  `recover missing route` error and nothing is dispatched. It is never treated as
  chat. Each rejected attempt shows as a failed `route` event.
- The route request is bounded by `persona_routing_timeout`.
- When background tasks (`delegation_enabled`) add tools to the turn, or the
  request does not end with the user's message, the turn uses the route tool as
  under `route_only`.
- With `voice_tools_enabled: false` and no tiered workers there is no route step
  in any mode. Decision shortcuts and skill selection run before routing and
  are unchanged.

### Comparing `route_only` and `json_route`

`just bench worker-decision -compare router -cases cases.json -out new.jsonl -model <model>`
replays labelled requests under both values against a local Ollama, alternating
which runs first. See [the bench README](../scripts/worker_decision_bench/README.md)
for the case format. Compare per mode:

- **Routing accuracy**: the share of samples with `correct: true`, and any `error`.
- **The reply's prompt read**: in each sample's `requests`, the `plain` entry is
  the reply. Compare its `prompt_eval_ms` between modes. Do not rely on
  `prompt_eval_count`: on Ollama's llama.cpp server backend it includes tokens
  served from the cache, so it barely moves when the prefix is reused.
- **Latency**: `first_text_ms` and `elapsed_ms`.

Keep `route_only` unless routing accuracy holds on the models you use.

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

Ollama tool-result projection, bounded archive, automatic compaction, and
continuation recovery settings are described in [Context continuity](context-continuity.md).

## Background tasks

Ollama can delegate managed work to Obey with `delegation_enabled: true`. Setup,
backend requirements, and scheduled result delivery are documented in
[Background tasks](delegation.md). The feature is off by default.

When Workers Settings are in **tiered** mode, the saved
`voice_workers.policy.concurrent_workers` / `queued_workers` and each tier's
`runtime_seconds` are the effective scheduling and runtime limits. The legacy
`voice_worker_max_concurrent` and `voice_worker_job_budget` keys remain
available for legacy mode only; enabling tiered mode does not silently migrate
or reinterpret those explicit singleton values.

Existing saved `queued_workers: 1` remains an explicit one-job queue bound.
Increase it in Workers Settings if desired; loading an older policy does not
silently replace that saved value with a newer default.

### Experimental group participation

`group_participation_enabled` defaults to `false` and takes effect in a new
conversation/runtime. It requires local Ollama inference.
`group_participation_mode` sets a new conversation's initial mode: `invited`
(default), `off`, or `open`. The corresponding environment variables are
`GROUP_PARTICIPATION_ENABLED` and `GROUP_PARTICIPATION_MODE`.

Conversation controls save mode and member changes with that session, without
changing global defaults. See [Group participation](personas.md#group-participation-experimental).

### Hosted persona sharing

`hosted_persona_sharing` (environment `HOSTED_PERSONA_SHARING`) chooses what a
persona on Claude receives when it joins a conversation shared with other
personas: `from_invite` (default) sends only what is said from the point
the persona is first addressed on that backend, plus the task and result of
each background job it started earlier, on any backend; `whole_conversation`
sends the spoken lines from the start. Tool calls, tool output and attachment names of
other personas are never sent in either mode. Changing it applies to personas
first addressed after the change and does not widen a session that has already
started. A running conversation picks the change up only when its settings
reload; it has no Settings row, so set it with
`obey-voice config set hosted_persona_sharing whole_conversation`. It governs
personas on Claude, Codex, Muse Spark and Obey; a persona on Grok or Cursor only
holds a conversation that started on it. Only `whole_conversation` widens what
is sent; any other value, including an unrecognised one from the environment or
a hand-edited file, sends from the invitation. See
[Hosted backends](personas.md#hosted-backends) for what each receives and for
how hosted personas are marked.

### Decision model

`decision_enabled` (default `false`) turns on the optional decision model, and
three switches choose what it answers: `decision_voice_work` and
`decision_text_work` (worker routing) and `decision_addressee` (which persona a
message naming one is for). All three default to `true`. With all three off,
nothing is prepared even when `decision_enabled` is on: no credential is read and
no model is installed, loaded or contacted. These keys have no environment
variables; `DECISION_API_KEY` is read only for an enabled hosted provider.

The addressee question sends the message (or a persona's reply, when checking
for a handoff), up to eight earlier messages (each cut
to 1,000 characters and labelled with its speaker's name), the speaker's name,
the current persona's name, and every persona name and alias in the conversation
to the configured `decision_provider`. With `local` that stays on this computer;
with `jev` or `openai` the hosted service receives it. Turn the question off with
`obey-voice config set decision_addressee false`. The full list, thresholds and
fallback order are in [Decision routing](decision-routing.md#who-is-being-addressed),
and why the question is on by default is in its
[upgrade note](decision-routing.md#upgrade-note-on-by-default).

### Ollama thinking in the terminal

In Settings → Brain → Thinking, select `false` to disable model thinking or
`true` to enable it. `auto` currently resolves to off. The `low`, `medium`,
`high`, and `max` levels are available for models that support those values.
The selection saves `ollama_think` and applies to the next conversation.
This controls generation; Ctrl+K and `/think` only show or hide thinking text
in the conversation history.
