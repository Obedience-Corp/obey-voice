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

> **Upgrading:** installs that saved settings while `claude_max_session_tokens` defaulted to `60000` still carry that value in `config.yaml` and keep the silent session drops. Delete the key (or set `0`) to adopt the warn-only default. `obey-voice doctor` flags this.

| Key | Default | Environment | Description |
|-----|---------|-------------|-------------|
| `active_persona` | `obey-voice` | `ACTIVE_PERSONA` | Persona profile id under `personas/<id>/` |
| `agent_name` | `Samantha` | | Display name (overlaid from active persona) |
| `persona` | `obey-voice` | `PERSONA` | Prompt document name for kind=persona (overlaid from active persona) |
| `compact_prompt` | *(embedded default)* | `COMPACT_PROMPT` | Prompt document name for kind=compact — the `/compact` summarize-turn instruction |
| `brain_provider` | `ollama` | `BRAIN_PROVIDER` | Brain backend: `ollama`, `claude`, or `grok` |
| `ollama_model` | empty | `OLLAMA_MODEL` | Ollama model name |
| `ollama_embedding_model` | `nomic-embed-text` | `OLLAMA_EMBEDDING_MODEL` | Ollama embedding model used to match each user prompt to relevant Agent Skills. Set empty to disable semantic routing and retain model-driven `read_skill` fallback. |
| `skills_similarity_threshold` | `0.55` | `SKILLS_SIMILARITY_THRESHOLD` | Minimum cosine similarity for automatic skill activation. Tune when using an embedding model with a different score distribution. |
| `ollama_host` | `http://localhost:11434` | `OLLAMA_HOST` | Ollama server URL |
| `ollama_num_ctx` | `8192` | `OLLAMA_NUM_CTX` | Context window requested on every chat call. `0` uses the server model default, which silently truncates long prompts from the top (system prompt first). |
| `ollama_keep_alive` | `10m` | `OLLAMA_KEEP_ALIVE` | How long Ollama keeps the model resident between turns (Go duration; empty = server default). |
| `ollama_think` | `false` | `OLLAMA_THINK` | Ollama thinking/reasoning for models that support it (qwen3.x, …). Voice defaults off so the model returns speakable content; empty content was previously replaced with the “lost my train of thought” fallback. Set `true` or `low`/`medium`/`high`/`max` to re-enable. |
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
| `tts_provider` | `kokoro` | `TTS_PROVIDER` | TTS backend |
| `voice_fallback_provider` | `kokoro` | `VOICE_FALLBACK_PROVIDER` | One-sentence runtime fallback after the selected provider fails; set empty/disabled to turn it off |
| `tts_voice` | `af_heart` | `TTS_VOICE` | Kokoro voice name |
| `tts_place` | `local` | | Where Qwen speech runs: `local` (this computer) or `lan` (a speech worker on another computer). Host topology, never a persona field |
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
| `transcribecpp_binary` | `transcribe-cli` | `TRANSCRIBECPP_BINARY` | transcribe.cpp CLI executable; a bare name is looked up on `PATH` |
| `transcribecpp_model` | `parakeet-tdt-0.6b-v2` | `TRANSCRIBECPP_MODEL` | GGUF the `transcribecpp` provider loads. This is upstream's full model slug, so it is spelled longer than `parakeet_model`'s `tdt-0.6b-v2` — not a typo |
| `vad_enabled` | `true` | | Enable voice activity detection |
| `vad_silence_duration` | `0.5` | | Seconds of silence before ending speech (raise to stop being cut off) |
| `vad_threshold` | `0.6` | `VAD_THRESHOLD` | Speech-detection confidence (raise to ignore background noise) |
| `vad_min_speech_duration` | `0.25` | `VAD_MIN_SPEECH_DURATION` | Minimum speech length in seconds (raise to ignore brief noises) |
| `voice_frontend_enabled` | `false` | `VOICE_FRONTEND_ENABLED` | Local AEC/NS/AGC on mic input (off by default: the noise suppressor currently over-suppresses normal-volume speech; enable only with barge-in) |
| `tui_mouse_enabled` | `false` | `TUI_MOUSE_ENABLED` | Claim the mouse so the wheel/trackpad is routed to the transcript viewport. Off by default so the terminal keeps click-drag selection and copy; turning it on means selecting needs a modifier (option/fn/shift). Either way `Page Up`/`Page Down` scroll, and with it off the wheel scrolls whenever the composer is empty. Applies on next enter/leave of conversation (Settings → Tools toggle or re-entry); no full process restart required for the TUI claim |
| `agent_name` | `Samantha` | | Display name |
| `persona` | `obey-voice` | `PERSONA` | Prompt document name for the interactive persona |
| `prompts_dir` | empty | `PROMPTS_DIR` | Prompt document directory; defaults to `~/.obey/agents/voice/festival-voice/prompts` when unset |
| `skills_enabled` | `true` | `SKILLS_ENABLED` | Agent Skills (`SKILL.md`) discovery for Ollama. Default on; set `false` to disable. Discovers project/workspace/user skill frontmatter into the system prompt; not a pre-activation sandbox. Claude/Grok already discover skills via their CLIs. |
| `skills_dir` | empty | `SKILLS_DIR` | Extra Obey Voice skills root (after project/workspace/user harness dirs); defaults to `~/.obey/agents/voice/festival-voice/skills` when unset. |
| `work_dir` | empty | `WORK_DIR` | Directory the agent's file and shell tools start in. **A default, not a sandbox** — a tool may still reach an absolute path outside it. Empty uses the directory Obey Voice was launched from; the binary's own folder, `/`, and any path that is not an existing directory fall back to your home folder. `obey-voice serve --work-dir DIR` overrides it for one process, and an override that is not an existing directory is a startup error rather than a silent fallback. |
| `models_dir` | `~/.cache/festival-voice/models` | `MODELS_DIR` | Model download directory |
| `language` | `en-US` | | Recognition language |
| `max_history` | `10` | | Saved conversation history length |
| `listen_timeout` | `10` | | Listen timeout in seconds |
| `phrase_time_limit` | `30` | | Maximum phrase length in seconds |

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

Two things are needed. Put `transcribe-cli` on `PATH` (or point
`transcribecpp_binary` at it) — there is no Homebrew formula, so use an upstream
release build — and run `obey-voice models ensure` to fetch the GGUF. The binary,
the GGUF and the GPU lane are all checked at startup and by `obey-voice doctor`,
which reports them as `transcribecpp-binary`.

The GGUF weights are Parakeet TDT 0.6B v2, licensed **CC BY 4.0** and
redistributed as GGUF by
[handy-computer/parakeet-tdt-0.6b-v2-gguf](https://huggingface.co/handy-computer/parakeet-tdt-0.6b-v2-gguf);
the transcribe.cpp runtime itself is MIT. `models ensure` and `models status`
print that attribution alongside the asset. The license covers the weights you
download, not this project.

Each utterance is one short-lived CLI process with its own model load, so the
per-utterance cost includes loading the GGUF — the win measured in the A/B is
compute, not end-to-end warm latency.

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

Use `obey-voice config migrate --dry-run` to preview the explicit
`stt_provider`/`stt_mode` values that would preserve the current STT behavior.
Dry runs report the config path and proposed values without writing files. Use
`obey-voice config migrate --write` to apply the migration; it creates a
timestamped `.bak` file before replacing an existing config. The write path
updates YAML via `yaml.v3`, so comments and unrelated keys are preserved where
possible, but scalar formatting around touched keys may be normalized.
