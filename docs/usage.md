# Usage

[Back to README](../README.md)

## Local voice (TUI)

Mic and speakers on this machine:

```bash
obey-voice            # Launcher → full conversation TUI with voice
obey-voice --no-tui     # Start conversation directly (no launcher)
obey-voice --text       # Text input, voice output
obey-voice --no-voice   # Voice input, text output
```

## Remote access (phone / another device)

Any device on the same Tailscale tailnet can use remote voice. Open the Obey Voice
TUI and choose **Use on another device**. Pick **Tailscale** or **Same Wi‑Fi**,
then open the link on any phone, tablet, or laptop. The TUI shows pairing and
client setup when the mic needs trusted HTTPS, and stops when you leave
that screen.

The equivalent CLI path remains available for headless use:

```bash
# Easiest remote access over Tailscale (MagicDNS URL; real cert when available):
obey-voice serve --tailscale

# LAN (self-signed TLS; iOS Safari mic needs a real cert):
obey-voice serve

# Ops
obey-voice serve --revoke-tokens   # Invalidate the bearer; next serve mints a new one
obey-voice connect <host:port> --token <token>   # Debug text client
```

If a trusted cert is not available yet, remote access still starts in
**limited** mode: text works on every device; most desktop browsers can use
voice after one warning; some mobile browsers need trusted HTTPS. The TUI/CLI
print a **Client setup** link (`https://login.tailscale.com/admin/dns` →
enable **HTTPS Certificates**), then restart. Same flow for LAN or Tailscale.

On the client: open the printed URL → enter the pairing code → **Start** →
**Hold to Talk** (or type). Protocol for custom clients:
[serve-protocol.md](serve-protocol.md).

| Path | Command | Where audio lives |
|------|---------|-------------------|
| Full local voice | `obey-voice` (TUI) | This machine’s mic + speakers |
| Remote keyboard only | Termius/SSH → `obey-voice` | Still this machine |
| Remote voice (LAN/tailnet) | TUI → **Use on another device**, or `obey-voice serve` / `--tailscale` | Client mic + speakers via WebSocket |

## Commands

```bash
obey-voice config                         # View all config
obey-voice config tts_voice af_bella      # Set a config value
obey-voice config schema --json           # Describe every key (types, bounds, help)
obey-voice config get --json              # Every effective value with its source
obey-voice config get tts_voice --json    # One value, its type, and its source
obey-voice config set tts_voice af_bella --json   # Change one key, surgically
obey-voice config unset stt_mode --json   # Remove a key so its default applies
obey-voice config migrate --dry-run       # Preview explicit STT config migration
obey-voice config migrate --write         # Apply STT config migration with backup
obey-voice persona list                   # List voice agent personas
obey-voice persona show                   # Show the active persona profile
obey-voice persona use samantha           # Switch active persona (persists)
obey-voice voices                         # List available Kokoro voices
obey-voice voices --locale en-US          # Filter voices by locale
obey-voice voices --json -p qwen3-tts     # Qwen presets from the static catalog (no model load)
obey-voice voices --json -p qwen3-tts --live  # Ask the installed native package instead (~10s)
obey-voice providers                      # Show brain, TTS, and STT providers
obey-voice test                           # Test microphone and speaker
obey-voice benchmark --prompt "hello"     # Run a local benchmark
obey-voice resume <session-id>            # Resume a saved session
obey-voice continue                       # Continue the most recent session
obey-voice doctor                         # Diagnose config, assets, and binaries (read-only)
obey-voice models status                  # Which model assets are installed vs missing
obey-voice models clean --unused --dry-run # Review what would be deleted, and what is kept
obey-voice models clean --unused --yes    # Delete the reviewed list (--plan when scripted)
obey-voice prompts list                   # List embedded and user prompt documents
obey-voice prompts show persona           # Show an assembled prompt document
obey-voice render notes.txt --out a.wav   # Batch-render a document to audio
obey-voice scene script.yaml --out-dir out/scene  # Record a two-persona conversation
obey-voice library list                  # Browse Calibre library (opt-in)
obey-voice library search "cryptography" # Search Calibre library (opt-in)
obey-voice library show 42               # Show one book's metadata
obey-voice serve --tailscale              # Remote voice for Tailscale clients
obey-voice serve --revoke-tokens          # Rotate serve bearer token
obey-voice serve --log-file serve.log     # Append human/diagnostic output to a file (0600)
```

## TUI controls

The launcher offers the most recent conversation first, a scrollable recent
session list, an explicit new-conversation action, and a managed Tailscale
remote server screen. The remote screen exposes the URL and single-use pairing code,
supports copy/restart controls, and owns server shutdown. During a conversation,
the transcript follows new messages until you scroll away from the tail. Chat
and the activity timeline are separate full-width views, so the transcript does
not lose space in wide terminals. The composer supports wrapped, multiline
drafts and compacts to one row in short terminal splits. Type `/` to open the
command palette, use the arrow keys to select a match, and press `Enter` to run
the highlighted command (or `Tab` to complete it into the composer first).
`/help` lists every available command. `/settings` opens the TUI settings
screen and returns to the conversation when you press `Esc` or `q`. Slash
commands are local — they do not cancel speech recognition or block the chat.

| Key | Action |
|-----|--------|
| `Enter` | Send the current draft |
| `Ctrl+J` | Insert a newline in the draft |
| Mouse wheel / trackpad, `Page Up` / `Page Down` | Scroll the transcript or focused activity feed |
| `Ctrl+T` | Focus/unfocus the activity timeline |
| `Ctrl+Y` | Copy the last assistant reply to the clipboard |
| `Ctrl+C` (empty composer, after a reply) | Copy the last assistant reply; press again within ~1.5s to quit (`/quit` also exits) |
| `Ctrl+G` | Pause/resume voice input (capture may stay armed; listening stops) |
| `Ctrl+O` | Mute/unmute spoken responses (also stops current playback) |
| `Home` / `End` | Jump to the start/end of the focused feed (on Chat, only when the composer is empty) |
| `Ctrl+Home` / `Ctrl+End` | Always jump to the start/end of the focused feed |
| `/copy` | Copy the last assistant reply |
| `/copy all` | Copy the full plain-text conversation |

Select transcript text with the mouse and copy it the way you would anywhere
else in your terminal — Obey Voice leaves the mouse alone, so click-drag
selection and your terminal's own copy both work without a modifier.

Scrolling still works: `Page Up` / `Page Down` always, and the wheel scrolls
the transcript whenever the composer is empty (terminals send arrow keys for
wheel turns when no program has claimed the mouse). `Up` / `Down` move the
cursor instead once you start drafting. `/copy`, `Ctrl+Y`, and empty-composer
`Ctrl+C` remain the quickest way to grab a whole reply.

If you would rather the wheel go straight to the viewport — smoother scrolling
while drafting, at the cost of needing **option** (iTerm2), **fn**
(Terminal.app), or **shift** (kitty, GNOME Terminal, Windows Terminal) held to
select — turn on **Settings → Tools → Mouse scroll** (or set
`tui_mouse_enabled: true`).

`/vim` enables modal composer editing (`/vim off` disables it). The input label
and footer change with the active mode. In NORMAL mode, use `i`/`a`/`I`/`A` to
enter INSERT, `h`/`j`/`k`/`l` and `w`/`b` to move, `x`/`D`/`dd` to delete,
`o`/`O` to open lines, `u` to undo, and `Enter` to send. `Esc` returns from
INSERT to NORMAL.

Microphone and speaker devices can be selected from the **Input** and
**Output** sections in TUI Settings. The **TTS** section selects the active
text-to-speech provider and shows its configured model context. Kokoro exposes
the static voice picker. Selecting an uninstalled **Qwen3-TTS** row installs the
native multi-tier package (worker binary + GGUF under `models_dir/qwen3-tts`;
uses the published platform release by default). After setup, the **Voice** section
lists Qwen's nine CustomVoice-class preset speakers; press `p` to preview and
`Enter` to select one. The **Language** section selects Qwen's synthesis language
(use **Auto** unless a book or conversation needs an explicit language). Returning
from Settings replaces the provider used by subsequent utterances in an
already-running conversation; no Obey Voice restart is required. The launcher
and conversation header show the active TTS provider/model/mode/voice badge.
An empty device config value follows the current operating-system default.

The first Qwen installation is a large download. It is isolated below
`models_dir/qwen3-tts`, can be inspected with `obey-voice models status --tts`,
and can also be installed non-interactively after selecting Qwen with
`obey-voice models ensure --tts`. Obey Voice keeps the model loaded in one native
warm worker for the lifetime of the provider. Advanced users can set both
`qwen_tts_binary` and `qwen_tts_model` to point at an explicit worker/model dir.

## Personas (voice agents)

A persona is a complete voice agent: its system prompt, brain provider/model, and
TTS provider/voice all belong to the persona rather than to global config. TUI
**Settings** writes global defaults only; to change one agent, edit it under
**Personas** (or its `personas/<id>/persona.yaml`). Any field a persona leaves
empty inherits the app default — an empty `brain:` uses the app-level
`brain_provider`/model, and an empty `tts:` uses the app-level TTS keys.

Starting a new conversation opens a persona picker: the active persona is
pre-selected, and `+ Create persona…` clones the current global stack into a new
profile. A conversation binds its identity — name, prompt, brain, and voice — at
start, so editing a persona or switching the active one never affects a session
already in flight.

The persona system-prompt editor has a vim mode: `esc` for NORMAL; `i`/`a`/`o`
to insert; `h`/`j`/`k`/`l`/`0`/`$`/`w`/`b`/`gg`/`G` to move; `x`/`D`/`dd` to
delete; `:w` accepts the prompt and advances to Model & voice; `:wq` (or
normal-mode `enter`) saves the whole form; `:q` cancels. `ctrl+j`, `alt+s`,
and `f2` also save the form from any step.

## Turn recovery and token usage

A hard tool or brain failure always ends with a spoken recovery reply ("I hit an
error while working on that…") while the error detail goes to the activity feed;
the turn is reported as `completed (degraded)` rather than dropped silently.

For Ollama, Activity records per-request `prefill N tok · gen M tok`. Prefill
tracks the size of your new turn, not the whole transcript, and is bounded by
`ollama_num_ctx` (default `8192`). Startup warmup sends that same `num_ctx` —
omitting it lets Ollama allocate the model's full window (262k on qwen3/kimi)
and a serve restart loop will pin a runner per spawn. `ollama_keep_alive`
(default `10m`) keeps the
model resident between turns; `ollama_think` (default `false`) disables
chain-of-thought on thinking models so voice turns get speakable `content`
instead of empty replies after a private think block.

See [Configuration](configuration.md) for the matching keys.
