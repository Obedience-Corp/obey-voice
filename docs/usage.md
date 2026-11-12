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
obey-voice connect <host:port> --token <token>   # Debug text client; system-trusted TLS
# Private/self-signed certificate, copied from the server through a trusted channel:
obey-voice connect <host:port> --token <token> --ca-cert ./server-cert.pem
```

`connect` verifies the server certificate before sending the token. For managed
self-signed servers, copy `serve/cert.pem` from the server's Obey Voice config
directory; never copy `key.pem`. `--fingerprint <sha256>` optionally adds a leaf
certificate pin, but no longer bypasses chain, validity, or hostname checks.
Use the hostname or IP covered by the certificate. See
[debug client trust setup](serve-protocol.md#debug-client-certificate-trust).

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
obey-voice config schema tts_voice --json # Describe one key; an unknown key lists close matches
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
obey-voice models usage                   # Where models are stored and how much space they use
obey-voice models move /Volumes/Big/models # Move every model to another folder and use it from now on
obey-voice models move --dry-run /Volumes/Big/models # Show what a move would do and change nothing
obey-voice transcribe --request job.json --jsonl   # Transcribe a finished WAV to timed JSONL
obey-voice transcribe capabilities --json # What this install can transcribe (read-only)
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

### Moving models to another drive

Models are stored in one folder (`models_dir`, by default
`~/.cache/obey-voice/models`). To put them on a larger drive, run
`obey-voice models move <folder>`, or use **Settings → Storage → Move models**
in the terminal app or **Settings → Models → Model storage** in the desktop
app. Both apps check the move first and show what it will do (size, file
count, and anything it will keep) before you confirm.
`obey-voice models move --dry-run <folder>` prints the same check and changes
nothing, not even a lock file.

The destination must be a full path to a folder that is empty or does not
exist yet. If it is a link, the files land in the folder it points to and the
link is kept.

**How it moves.** On the same drive the folder is renamed; until the new
folder is saved, a link at the old path leads to it. An empty destination you
made yourself is replaced by the moved folder, so its own permissions are not
kept. On another drive the files are copied and flushed to disk, then
`models_dir` is saved together with any other setting that named a file in
the old folder. Only then is the old folder set aside, as
`<folder>.obey-voice-old-<time>` next to where it was, and every file in it is
read again and compared, by content, with what was copied. Anything that
changed is copied again. This check reads the whole old folder once more, so
it takes about as long as reading that much from the old drive (hashing runs
at about 3 GB/s on an Apple silicon Mac, faster than most drives read). The
set-aside folder is deleted only if a second look
finds nothing still changing. Copies keep each file's permissions and
modification time, not extended attributes or ACLs.

**When the old folder is kept.** The set-aside folder is kept, with a
`README-obey-voice-move.txt` inside, and the move says why, when something in
it was still changing, when a check failed, or when it holds entries that
nothing in your settings or personas uses. Such entries are moved too, but
they may be another program's files, or models you no longer use. The
confirmation and `--dry-run` list them with their sizes. To free the old
drive completely, review `obey-voice models clean --unused --dry-run` first,
or delete the kept folder yourself once you have checked it.

**Cancelling.** Cancel, or Ctrl-C, works until the new folder is saved: the
old folder stays in use and only what the move created is removed (an empty
destination you made yourself is kept). After the save the move finishes. A
cancelled `models move` exits with status 130.

- **Terminal (`models move`):** SIGTERM and a closed terminal also cancel and
  let the cleanup finish. A second Ctrl-C quits at once.
- **Terminal app:** leaving Settings or quitting cancels the move. Quitting,
  however it happens (q, Ctrl-C, `kill`, closing the terminal), waits for the
  cleanup before the app exits; Ctrl-C during that wait quits at once.
- **Desktop app:** Cancel sends the move one stop request and waits for its
  cleanup; it never forces it. Quitting the app sends the same request and
  leaves the move to finish on its own. On Windows, Cancel and quitting stop
  the move at once with no cleanup.

**After a hard kill** (power loss, `kill -9`, Ctrl-C twice, Cancel or quit on
Windows) nothing is lost, but something may be left behind:

- during the copy: a partial copy in the destination, marked by
  `.obey-voice-move-incomplete`. The old folder is still in use. A later move
  to that folder names the folder holding the partial copy; delete what is
  inside it.
- after a same-drive rename, before the save: a link at the old path leading
  to the new folder, and everything keeps working through it. Only a kill in
  the instant between the rename and the link leaves `models_dir` naming a
  folder that is gone; the models are then in the destination, and
  `obey-voice config set models_dir <destination>` points at them.
- after the save: the old folder, set aside as `<folder>.obey-voice-old-<time>`
  and perhaps partly deleted, or on the same drive a link at the old path. The
  new folder is complete and in use.

**The move is refused, and nothing is changed, when:**

- `models_dir` is set by `MODELS_DIR` or is a relative path;
- `models_dir` is your home folder, a standard folder such as Documents, the
  top of a drive, or contains the obey-voice settings folder; or the home
  folder cannot be found;
- either folder is inside the other;
- another setting set in the environment (for example `WHISPERCPP_MODEL_PATH`)
  names a file in the old folder, or `config.yaml` cannot take the new values;
- the folder holds something other than files, folders and links, links that
  would point somewhere else after the move, or another drive mounted inside
  it;
- the other drive does not have room for the copy;
- a decision model install, `obey-voice serve`, or another move is running.

**What the move cannot see.** It cannot tell that another program is reading
or writing the folder: a conversation or meeting in another window, the
desktop app's engine, a download in progress, or a `serve` started by an
older build or on Windows. A program that keeps a file open can still write
to it after the final check. Stop them first. The terminal app refuses a move
while its own conversation or meeting is running, and starts neither while a
move is finishing.

To start using a folder that already holds models, set `models_dir` instead;
that changes the setting and moves nothing.

### Transcribing a recording

`obey-voice transcribe` turns a finished 16 kHz mono WAV into timed utterances
on stdout, one JSON object per line, for another program to consume. It starts
no conversation, opens no microphone and downloads nothing.

```bash
obey-voice transcribe capabilities            # is this machine ready
obey-voice transcribe --request job.json --jsonl
```

The request document, the events, the failure codes and the client rules are in
[the protocol reference](transcribe-protocol.md). Keepshot uses it to write
`.txt` and `.vtt` sidecars beside a recording.

## TUI controls

The launcher's logo rotates while the menu is visible and the terminal has
room for it. Set `OBEY_VOICE_REDUCED_MOTION=1` for a still logo and reduced
animation throughout the TUI.

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
During a conversation, background work continues while you talk or type.
The animated **Working** cue stays in the top-right header beside the voice
meter and in the footer; queued work has a static cue. Click the header cue
(with TUI mouse controls enabled) or press `Ctrl+W`. One active job opens its
detail directly; several active jobs open the worker list. The cue clears when
the job ends. The chat's worker entry shows each tool's command or target,
status, output preview and error, and retains those details after completion.
`Ctrl+W` also opens the newest completed worker when none are active.

Starting a worker updates its job status without inserting or speaking a canned
"Working on it" reply. The worker's generated result arrives when ready. If an
Ollama turn exhausts its ten tool rounds, it gets one final model request with
tools disabled to answer from the collected results and explain any blockers.
Existing request, runtime and token budgets still apply. A failed or empty final
answer is reported as a failure rather than a successful result.

**Allow background workers** (`t`) controls new jobs immediately and is saved
for future chats. Turning it Off does not cancel already-running or queued
tasks; use **Stop all** for that. The same toggle is in **Settings → Worker**.
Task details report when termination is unconfirmed. The chat footer also shows
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
| `Ctrl+W` | Open the active worker detail, or choose from the worker list |
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
the language. When the voices state more than one locale, a **Language filter**
row above them (`enter`/`+`/`-` step it) narrows the list to one; the ✓ voice
always stays listed, and the filter is not saved. For `openai-speech` the
voices come from the server's own catalog
([Voices and personas](openai-speech.md#voices-and-personas)). Choosing an
uninstalled **Qwen3-TTS** engine installs the native multi-tier package (worker
binary + GGUF under `models_dir/qwen3-tts`) when a published platform tarball
exists (macOS Apple Silicon Metal, or linux/amd64
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

The TUI and desktop conversation headers also show the active persona's brain provider and
model, for example `Obey  brain ollama · qwen3:8b`. It follows persona switches
within the conversation. `default` means the provider chooses its own model;
it does not identify a specific underlying model.

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

Prompt, model, voice, and shared conversations are in [Personas](personas.md).

## Turn recovery and token usage

Text-only error reporting and Ollama token notes are in [Turn recovery](turn-recovery.md).
