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

# Same machine: start serve and open the speaking Stage orb:
obey-voice serve --open

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
obey-voice persona enable veronica-hype   # Allow an experimental persona to be selected
obey-voice persona disable veronica-hype  # Exclude its names and aliases from routing
obey-voice voices                         # List available Kokoro voices
obey-voice voices --locale en-US          # Filter voices by locale
obey-voice voices --json -p qwen3-tts     # Qwen presets from the static catalog (no model load)
obey-voice voices --json -p qwen3-tts --live  # Ask the installed native package instead (~10s)
obey-voice voices register en --from-kokoro af_heart   # Audio8 voice from synthesized audio (no recording)
obey-voice voices register me --wav ref.wav --text "..." --consent  # ...or your own recording
obey-voice voices registered              # Registered Audio8 voices and where they came from
obey-voice voices remove me               # Delete one registered Audio8 voice
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
obey-voice scene beats.yaml --out-dir out/improv  # Let two personas improvise it from a brief (mode: generate)
obey-voice library list                  # Browse Calibre library (opt-in)
obey-voice library search "cryptography" # Search Calibre library (opt-in)
obey-voice library show 42               # Show one book's metadata
obey-voice serve --tailscale              # Remote voice for Tailscale clients
obey-voice serve --revoke-tokens          # Rotate serve bearer token
obey-voice serve --log-file serve.log     # Append human/diagnostic output to a file (0600)
```

Disabled personas remain listed and editable, but their names and aliases are
excluded from selection. They cannot be selected until enabled again. The
saved default persona cannot be disabled, and ambiguity among enabled names or
aliases remains an error. In `/personas`, select a row and press **x** to toggle
its state. Inactive rows remain editable and show an `[inactive]` badge. Choose
another saved default or conversation responder before disabling that persona.

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
`/personas` opens the persona manager to browse, select, create, or edit
personas. Voice input pauses while it is open; `Esc` or `q` returns to the
same chat and restores its previous microphone state. In a supported shared
conversation, selecting another persona changes the responder for the next turn
and keeps the current session, history and running work. Opening or cancelling
the manager does not change the responder. Selection from the launcher sets the
default for a new conversation instead.

`/workers` opens the background task manager (also available as **Workers**
on the main menu). Select a task and press `Enter` for its status,
provider/model, elapsed time, activity and output. Press `s` to stop the
selected task, or `x` then `y` to stop all running and queued tasks.
`Esc` returns from details to the list, then to the same chat or menu.
Voice input pauses while browsing and returns to its previous state.

**Allow background workers** (`t`) controls new jobs immediately and is saved
for future chats. Turning it Off does not cancel already-running or queued
tasks; use **Stop all** for that. The same toggle is in **Settings → Worker**.
Task details report when termination is unconfirmed. The chat footer shows
compact counts and a `/workers` hint instead of the task and tool summary.

`/help` lists every available command. `/settings` opens the TUI settings
screen and returns to the conversation when you press `Esc` or `q`. Slash
commands are local. Opening a manager pauses listening; leaving it restores
the previous microphone state.

`/menu` (or `/back`) ends the current chat and returns to the main menu. The
session is saved before the menu opens; choose **Continue** to resume it or
**New conversation** to start fresh. `/quit` still exits Obey Voice.

| Key | Action |
|-----|--------|
| `Enter` | Send the current draft |
| `Ctrl+J` | Insert a newline in the draft |
| Mouse wheel / trackpad, `Page Up` / `Page Down` | Scroll the transcript or focused activity feed |
| `Ctrl+T` | Focus/unfocus the activity timeline |
| `Ctrl+K` | Show or hide model thinking blocks in the thread (`/think`) |
| `Ctrl+R` | Start or stop Talk recording (`/record talk` / `/record off`). Lab is `/record lab`. Vim mode keeps Ctrl+R as redo. |
| `Ctrl+Y` | Copy the last assistant reply to the clipboard |
| `Ctrl+C` (empty composer, after a reply) | Copy the last assistant reply; press again within ~1.5s to quit (`/quit` also exits) |
| `Ctrl+G` | Pause/resume voice input (capture may stay armed; listening stops) |
| `⌃Space` (macOS) / `Ctrl+Alt+Space` (Windows), any app focused | Talk key: hold to talk and release to send, by default. Rebind it in Settings → Tools. On Linux, bind your own shortcut to `obey-voice talk` — see below |
| `Ctrl+O` | Mute/unmute spoken responses (also stops current playback) |
| `Home` / `End` | Jump to the start/end of the focused feed (on Chat, only when the composer is empty) |
| `Ctrl+Home` / `Ctrl+End` | Always jump to the start/end of the focused feed |
| `/copy` | Copy the last assistant reply |
| `/copy all` | Copy the full plain-text conversation |
| `/think` | Show or hide model thinking in the thread (`/think on`, `/think off`) |
| `/record` | Record this conversation: `/record talk` (events + mix later), `/record lab` (plus playback debug dumps), `/record off` to seal. Bundles land in `captures/` (`obey-voice captures`, `obey-voice paths --json`). |

### Talk key

The conversation gives you one system-wide way to speak to it from whatever app
is focused, instead of finding the terminal for `Ctrl+G`. Only the key is held
outside the conversation; the microphone stays where it is, in the pipeline.

| Mode (`talk_hotkey_mode`) | Press | Release |
|---|---|---|
| `hold` (default) | opens your turn | sends it |
| `toggle` | opens your turn; the next tap sends | nothing |
| `automatic` | opens your turn | a press shorter than 300 ms is a tap (next tap sends); longer is a hold (release sends) |

Releasing sends what was heard even if you said nothing; the pipeline reports
an empty turn and listens again. Pressing over a reply interrupts it first.

The key never changes the mute. While the mic is paused with `Ctrl+G`, a press
is one bounded turn: the mic opens for that press, the release sends, and the
session is still paused afterwards. When the mic is already listening, the
press adopts the open turn and the release commits it without waiting for the
silence timeout.

The mode is `talk_hotkey_mode`, the combo is `talk_hotkey` (empty means the
platform default), and `talk_hotkey_enabled: false` turns the whole thing off.
All three are rows in Settings → Tools: `enter` or `space` toggles the key, the
mode row shows all three modes and `enter` moves to the next, and `enter` on the
combo opens a one-line editor (`ctrl+alt+t`, `f13`; empty returns to the
default). Changes apply to the next conversation.

#### macOS

The conversation registers the combo itself — `talk_hotkey`, default ⌃Space,
the same key as the Obey Voice Mac app — through Carbon's `RegisterEventHotKey`,
which swallows it so it never types into the focused app and needs no
accessibility permission. ⌃Space is also "select the previous input source"
when more than one keyboard layout is enabled; pick another combo if you use
that.

macOS lets several apps hold the same hot key and fires every one of them. The
Obey Voice Mac app registers ⌃Space for itself, so while it is running the TUI
leaves that combo to it and says so in the activity panel; `obey-voice doctor`
reports the same. Give the TUI another combo (`talk_hotkey: ctrl+alt+space`,
or Settings → Tools) to use both at once, or move the app's key in its own
Settings → Hotkey. Raycast and Spotlight also default to a modifier plus
space; if a press does two things, that is why. When registration itself fails,
the conversation says so in its header and `Ctrl+G` keeps working.

#### Windows

The conversation registers the combo itself, through `RegisterHotKey`. The
default is Ctrl+Alt+Space, not Ctrl+Space: Ctrl+Space switches input methods
on Windows, and Alt+Space opens the window menu in every Win32 app. Unlike
macOS, a Windows hotkey belongs to one process — a combo
another program already holds fails outright, and the conversation reports
which combo is taken rather than silently doing nothing.

The Windows helper is compile-verified only; it has not been run on a Windows
machine.

#### Linux, and any tool you already use

Wayland compositors reserve keyboard grabs for themselves — that is the point
of the design, and it is why there is no Linux helper. Instead the conversation
listens on a private endpoint and your own shortcut tool drives it:

```
obey-voice talk down     open a spoken turn
obey-voice talk up       send it
obey-voice talk press    both at once — a tap
obey-voice talk ping     exit 0 if a conversation is listening, 1 if not
```

Bind `down` to the press and `up` to the release for hold-to-talk. A tool that
only reports presses (GNOME, KDE) binds `press` with `talk_hotkey_mode: toggle`,
so one tap opens the turn and the next sends it.

sway / i3:

```
bindsym --no-repeat $mod+space exec obey-voice talk down
bindsym --release $mod+space exec obey-voice talk up
```

Hyprland:

```
bind  = ALT, SPACE, exec, obey-voice talk down
bindr = ALT, SPACE, exec, obey-voice talk up
```

GNOME / KDE (custom shortcut, press only — set `talk_hotkey_mode: toggle`):

```
obey-voice talk press
```

The same commands work on macOS, so Raycast, Karabiner-Elements or Hammerspoon
can drive the talk key instead of the built-in combo.

The endpoint is per user and private: a unix socket in a `0700` directory under
`$XDG_RUNTIME_DIR` (or your temp directory, named with your uid), and on Windows
a loopback port plus a token recorded under `%LOCALAPPDATA%`. `obey-voice
doctor` prints the address in its `talk-control` row on every platform, and the
conversation names it in the activity panel when it opens. With no conversation
running, `obey-voice talk` exits 1 and says so; `obey-voice talk ping` is that
check on its own, with no turn attached.

One conversation owns the endpoint at a time. A second one started while the
first is running says so and leaves the first one's endpoint alone, so a bound
key keeps reaching the conversation that answered it.

### Name summon

Off by default (`name_summon_enabled`). Turned on, the open microphone stops
answering everything it hears and answers only what is addressed to the agent —
which is the point in a room where other people are talking.

An utterance is addressed when it **starts** with the agent's name: `agent_name`,
its first word when the name has several (`Uncle Fu` also answers to `Uncle`),
or any word in `name_summon_aliases`. Matching is case-insensitive and
whole-word, and one filler may come first — `hey`, `ok`, `okay`, `hi`, `hello`
or `yo`. So "Obey, what's the time?" and "hey obey what's the time" both open a
turn, while "what's for dinner?" and "I told Obey earlier" are dropped, with a
line in the conversation naming what was ignored.

The talk key always bypasses the gate: pressing it *is* addressing the agent, so
a held key answers whatever you say, name or no name. `Ctrl+G` mute keeps the
gate deaf — nothing is listening, so there is nothing to summon — and the agent
does not listen for its name while it is speaking. Talking over a reply is
barge-in, which has its own rules.

Name summon needs the voice front end on (`voice_frontend: nlms` or `webrtc`).
Without echo cancellation the microphone hears the speakers, so the agent would
hear its own name in its own playback and summon itself: `obey-voice doctor`
reports that as an error, the **Settings → Tools → Name summon** toggle refuses
to turn on, and a conversation started that way says so and runs without the
gate.

**What this version is not.** The gate reads the finished transcript, not the
audio, so the agent decides whether it was addressed only after you stop
talking. Recognition still runs on every utterance; the saving is in what gets
answered, not in what gets heard. A wake-word model that reacts to the name
itself is a later step — this is the version that needs no new model.

### Mouse, editing and Settings

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

The **Speakers** section holds chat speaker labels, your display name, and
every enrolled voice — **Register your voice** for you, **Enroll another
speaker…** for guests — three short clips recorded on the conversation’s
microphone so live labels say a name instead of `speaker-1` (see
[meetings.md](meetings.md#register-your-voice)).

TUI Settings is a sidebar of sections beside the section's lists: `←/→` (or a
digit) switch section, `↑/↓` move through the lines, and `enter` chooses the
option under the cursor. Microphone and speaker devices are two lists in the
**Audio** section with the current device marked ✓ and the volume slider below
them (`+`/`-`). The **Voice** section lists the text-to-speech engines with
each one's configured model context, then the voices of the active engine, then
the language. Choosing an uninstalled **Qwen3-TTS** engine installs the native
multi-tier package (worker binary + GGUF under `models_dir/qwen3-tts`) when a
published platform tarball exists (macOS Apple Silicon Metal, or linux/amd64
CPU) or `qwen_tts_native_url` is set; it is listed dim until then, so the
download is a deliberate choice. After setup, the **Voice** list shows Qwen's
nine CustomVoice-class preset speakers: `p` previews the one under the cursor,
`enter` chooses it. The **Language** list selects Qwen's synthesis language
(use **Auto** unless a book or conversation needs an explicit language).
Returning from Settings replaces the provider used by subsequent utterances in
an already-running conversation; no Obey Voice restart is required. The
launcher and conversation header show the active TTS provider/model/mode/voice
badge. An empty device config value follows the current operating-system
default.

The first Qwen installation is a large download on platforms that have a
published package. It is isolated below `models_dir/qwen3-tts`, can be inspected
with `obey-voice models status --tts`, and can also be installed
non-interactively after selecting Qwen with `obey-voice models ensure --tts`.
Linux/amd64 downloads the CPU package (`qwen3-tts-native` v0.1.1). It is
slower than realtime on a handheld; Kokoro stays the conversation default.
Obey Voice keeps the model loaded in one native
warm worker for the lifetime of the provider. Advanced users can set both
`qwen_tts_binary` and `qwen_tts_model` to point at an explicit worker/model dir.

## Personas (voice agents)

A persona is a complete voice agent: its system prompt, brain provider/model, and
TTS provider/voice all belong to the persona rather than to global config. TUI
**Settings** writes global defaults only; to change one agent, edit it under
**Personas** (or its `personas/<id>/persona.yaml`). Any field a persona leaves
empty inherits the app default — an empty `brain:` uses the app-level
`brain_provider`/model, and an empty `tts:` uses the app-level TTS keys.

Starting a new conversation opens a persona picker: **Use defaults** is always
the first option (the models, providers, and voice from Settings). Named
personas follow, with the active one marked, and `+ Create persona…` clones the
current global stack into a new profile. Each admitted response snapshots its
persona, prompt and voice. Switching changes subsequent responses without
retargeting the reply already in flight or relabeling earlier replies. Profile
edits are resolved at the next response boundary.

In the Personas list, `d` deletes the persona under the cursor. It always asks
first — only `y` confirms, any other key cancels — and it removes
`personas/<id>/` plus the system prompt that persona owns, keeping a prompt
document another persona points at. Built-ins and the last remaining persona
are refused. Deleting the active persona activates another one so the agent
name and voice keys stay consistent. The same removal from the shell is
`obey-voice persona delete <id> --yes`.

The create/edit form is one screen, not a wizard: Name, the Model & voice rows
(Brain, Model, TTS, Voice, Tier), and the System prompt. `↑`/`↓` (or tab) move
between rows, `←`/`→` change the provider, model or voice under the cursor, `p`
on the Voice row previews the selected voice (the same sample Settings uses;
`(default)` plays the inherited Settings voice), and `ctrl+j` (also `alt+s`,
`f2`, `ctrl+s`) saves from anywhere — so changing a persona's model or voice
never goes through the prompt editor. `esc` cancels, except while the prompt
differs from what the form opened with; that is discarded explicitly with `:q`.
From the Personas list, `e` opens the form on the name and `m` opens it straight
on Model & voice.

The System prompt row shows the document's opening line and its size; `enter`
gives the vim editor the whole screen: `esc` for NORMAL; `i`/`a`/`o` to insert;
`h`/`j`/`k`/`l`/`0`/`$`/`w`/`b`/`gg`/`G` to move; `x`/`D`/`dd` to delete; `tab`
in INSERT opens a dropdown of `{agent_name}`, `{user_name}`, `{date}`, and
`{os}` (type `{` first to filter); Enter inserts the token at the cursor; `:w`
returns to the form; `:wq` (or normal-mode `enter`) saves the persona; `:q`
discards the whole edit.

### Shared conversations

With a supported local Ollama persona, address another configured persona to
select it: `Santa.` gives a short acknowledgement in Santa's voice; `Santa,
what do you think?` selects Santa and answers the request. Santa remains the
responder for ordinary follow-ups without another name, including after quiet
gaps, microphone pause/resume or editing. Use the Talk persona control or TUI
`/personas` for explicit selection. `/speakers` still manages human speaker
labels; it does not choose the responding persona.

Incidental mentions such as `Santa is a persona` do not select Santa. Unknown
explicit targets, ambiguous addressed names and failed prompt/model/voice preparation
leave the old responder selected and report an error. A shared alias or generated
short name (for example, `Veronica` and `Veronica Hype`) does not block ordinary
follow-ups or requests addressed to another persona. Use a unique full name or
`/personas` when a spoken name is ambiguous. Response errors after speech was
recognized keep voice input enabled; only repeated input failures switch to text.
Shared-history projection
is currently verified for Ollama. An unsupported provider is refused rather
than silently replaced. The local acceptance uses Qwen3:8b and Kokoro voices
`af_heart` and `am_michael`; it does not establish parity for every provider.

Conversation identity and reconnect snapshots do not require
`voice_work_enabled`. That setting remains a separate opt-in for experimental
conversation-scoped work, with its existing provider/admission restrictions.
Switching a responder never changes that setting. The internal selection flow
is embedded and validated; editable persona profiles do not replace it.

### Saved history and recovery

The selected responder is saved with the session, separately from the default
for future conversations. Use `obey-voice sessions list`,
`obey-voice sessions show <id> --json`, and `obey-voice resume <id>` to inspect
and resume existing conversations. Changing a profile's display name preserves
old reply labels. If a saved profile is missing, the transcript remains readable
but a valid explicit selection is required before another response.

Session format2 retains canonical turns and immutable assistant identity.
Older sessions remain readable; missing identity stays unknown, including old
assistant-role names. A future unsupported format is refused. Compaction keeps
the canonical transcript and a `<id>.pre-compact.json` backup while producing
a bounded model context with complete tool groups and attributed source facts.
A stored transcript alone does not prove a model received all of it: its
configured history/context limits still govern the projection.

Before downgrading, stop processes that write the session and preserve the raw
session directory reported by `obey-voice paths --json`. Keep the original JSON
and any pre-compaction backup; an older binary may discard fields it does not
understand. The `sessions show --json` output is for inspection, not a raw-store
restore format. Restore raw backups only with writers stopped and a compatible
version. Clear starts a fresh conversation epoch; it is not persona selection.

The speech-routing proof includes actual installed STT on recorded input:
`Santa.` was normalized to `Santa` and still selected Santa, with and without
a custom-word hint. It does not measure physical microphone recognition.
Timing values for model completion, first audio and playback are milestones
from their named origin; do not add them as stage durations. The small local
proof sample is not a latency guarantee.

## Turn recovery and token usage

A hard tool or brain failure always ends with a spoken recovery reply ("I hit an
error while working on that…") while the error detail goes to the activity feed;
the turn is reported as `completed (degraded)` rather than dropped silently.

For Ollama, Activity records per-request `prefill N tok · gen M tok`. Prefill
tracks the size of your new turn, not the whole transcript, and is bounded by
`ollama_num_ctx` (default `32768` for talk; workers use `voice_worker_num_ctx` 8192). Startup warmup sends that same `num_ctx` —
omitting it lets Ollama allocate the model's full window (262k on qwen3/kimi)
and a serve restart loop will pin a runner per spawn. `ollama_num_batch`
(default `128`) decides how much of the prompt is re-read each turn. Ollama
otherwise derives it mainly from the context window — 512 at or below 4096, 1024
above, 2048 above 32768, downgraded toward 512 if the model's predicted VRAM
will not fit — and llama.cpp keeps its last reusable checkpoint about one batch back
from the end of the prompt, so a large batch means a conversational turn reuses
almost nothing and re-reads the persona and history from scratch. On
`qwen3.8-uncensored:27b` that cost 4.3 s before every spoken word; `128` skips
89 % of the prompt instead of 14 % and cut the median first token to 864 ms
(`docs/benchmarks/B2-qwen-ttft-mechanism-2026-08-28.md`). Brand-new prompts read
slightly slower, so this trades the tail for the median. `0` restores Ollama's
own choice. `ollama_keep_alive`
(default `10m`) keeps the
model resident between turns; `ollama_think` (default `auto`) turns thinking
on for qwen3-family models so chain-of-thought is not spoken. Explicit `false`
on qwen3 dumps untagged CoT into Content, which can be spoken.

See [Configuration](configuration.md) for the matching keys.
