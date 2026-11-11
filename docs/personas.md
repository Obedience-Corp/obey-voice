# Personas (voice agents)

[Back to README](../README.md)

A persona is a complete voice agent: its system prompt, brain provider/model, and
TTS provider/voice all belong to the persona rather than to global config. TUI
**Settings** writes global defaults only; to change one agent, edit it under
**Personas** (or its `personas/<id>/persona.yaml`). Any field a persona leaves
empty inherits the app default — an empty `brain:` uses the app-level
`brain_provider`/model, and an empty `tts:` uses the app-level TTS keys.

Profiles also accept an optional `audio` section for native audio implementation
work. Existing profiles omit it and keep Standard behavior. Native activation
is currently unavailable: a saved native draft is rejected before selection or
preference writes until its production input and transport bindings exist.

```yaml
audio:
  mode: standard              # standard (default) | native (not yet activatable)
  provider: ollama            # native provider; default ollama
  model: lfm3-audio:2b        # native model default
  endpoint: http://localhost:11434 # omitted = effective ollama_host
  voice: female              # omitted = runtime default
  controls: standard         # standard | conversation
  transcript: auto           # auto | required | off
  retain_audio: false
```

Standard mode can retain these inactive settings without changing the saved
brain, worker, STT or TTS choices. Native Standard controls require `auto` or
`required` transcription. Native conversation controls permit all three
transcript choices, but are incompatible with group participation; native mode
also rejects acoustic name recognition. Endpoints must be HTTP(S) server origins
or reverse-proxy prefixes without credentials, query strings, or `/api` or `/v1`
suffixes. Schema validation and candidate configuration resolution do not prove
model or voice support; capability checks and UI controls are still pending.

Starting a new conversation opens a persona picker: **Obey (default)** is always
the first option and loads Obey's saved prompt, brain, worker and voice. Unset
profile fields inherit Settings. Other named
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
(Brain, Model, TTS, Voice, Language, Tier), the Starting directory, and the
System prompt. `↑`/`↓` (or tab) move between rows, `←`/`→` change the provider,
model, voice, language or directory under the cursor, `p`
on the Voice row previews the selected voice (press `p` again to stop;
the same sample Settings uses;
`(default)` plays the inherited Settings voice), and `ctrl+j` (also `alt+s`,
`f2`, `ctrl+s`) saves from anywhere — so changing a persona's model or voice
never goes through the prompt editor. `esc` cancels, except while the prompt
differs from what the form opened with; that is discarded explicitly with `:q`.

In the desktop app, **Create persona** and **Edit** both offer **Preview voice**
beside the voice field. Preview uses the unsaved speech engine, voice, and Qwen
model tier, with empty fields inheriting app settings. It works before starting
a conversation and does not save or activate the persona. **Stop preview**,
changing the speech selection, saving, or closing the form cancels playback.
Preparation and playback failures appear in the form so you can adjust and retry.

The Voice row shows its position (`6/37`) once a catalog is longer than eight
voices. The Language row narrows it to one locale when the catalog states
locales (Kokoro's `en-us`/`en-gb`/…, Phonon's languages and `characters`); it
is a filter for picking, not a saved setting, and a voice with no locale (the
server's `default`) stays visible under every filter. For `openai-speech` the
catalog is fetched from the configured server when the form opens or the TTS
row lands on it; until it answers the row lists the configured voice plus
`default` with a "fetching" note, and a server that lists nothing keeps that
list with the reason under the row (see [openai-speech.md](openai-speech.md)).
The dim line under the Voice row describes the pick when the catalog can
(locale, gender, age, description).
From the Personas list, `e` opens the form on the name and `m` opens it straight
on Model & voice.

The System prompt row shows the document's opening line and its size; `enter`
gives the vim editor the whole screen: `esc` for NORMAL; `i`/`a`/`o` to insert;
`h`/`j`/`k`/`l`/`0`/`$`/`w`/`b`/`gg`/`G` to move; `x`/`D`/`dd` to delete; `tab`
in INSERT opens a dropdown of `{agent_name}`, `{user_name}`, `{date}`, and
`{os}` (type `{` first to filter); Enter inserts the token at the cursor; `:w`
returns to the form; `:wq` (or normal-mode `enter`) saves the persona; `:q`
discards the whole edit.

### Worker brains

Each persona's **Worker provider** can use any supported brain: Ollama, Claude,
Codex, Cursor, or Grok. Worker model choices follow the selected provider's
discovered models. Cursor requires `cursor-agent` installed and authenticated,
just as it does when used for conversation. Its background jobs use separate
conversation histories and CLI sessions.

The desktop persona editor and Settings use the same provider catalog as the
TUI. Tiered CLI workers still require explicit billing/account settings and
the existing paid-worker opt-in; selecting a provider does not enable paid work
or give it spending limits its adapter cannot enforce.
Cursor is unavailable for strict worker plans because its native tools cannot
be restricted to the strict allowlist. Direct strict-budget calls are rejected
before the CLI starts; ordinary legacy and explicitly uncapped advisory workers
remain available.

### Shipped personas

| Persona | Character | Speech provider | Voice |
|---------|-----------|-----------------|-------|
| Obey | Direct, practical assistant | `openai-speech` (Phonon) | `Harper` |
| Samantha | Warm, curious conversational companion | `openai-speech` (Phonon) | `Juniper` |
| Jarvis | Precise, dry British assistant | `openai-speech` (Phonon) | `Edwin` |
| Veronica | Playful conversational companion | `openai-speech` (Phonon) | `Zoey` |
| Santa | Warm storyteller with a practical workshop streak | `kokoro` | `am_santa` |

All five inherit the application brain unless you set one on the profile.
The shipped Obey profile pins Harper; **Obey (default)** uses the saved Obey
profile, including any voice or model you have chosen for it.
Santa is a selectable companion, not the startup persona.

Phonon profiles use the configured `openai_speech_url` and
`openai_speech_model`, which default to local Ollama and `phonon`. They require
a server that serves these named voices. The catalog and synthesis were checked
with Ollama 0.40.11; an older runtime or model that only offers `default` needs
an update and a fresh `ollama pull phonon`. See
[Phonon setup](openai-speech.md#phonon-through-ollama). Selecting a Phonon persona
does not make a different configured speech server serve Phonon automatically.

Missing profiles are added on load. Existing profiles and prompt overrides are
never replaced, including your own persona named Santa. To adopt the new voices
on existing profiles, use **Personas → Model & voice**, or:

```bash
obey-voice persona edit obey --tts-provider openai-speech --voice Harper
obey-voice persona edit samantha --tts-provider openai-speech --voice Juniper
obey-voice persona edit jarvis --tts-provider openai-speech --voice Edwin
obey-voice persona edit veronica --tts-provider openai-speech --voice Zoey --tier ""
obey-voice persona edit santa --tts-provider kokoro --voice am_santa
```

These commands change voice settings only. They keep each persona's name,
brain, prompt, and working directory. Disable a companion you do not want in
the group with `obey-voice persona disable <id>`.

### Names and recognition

The shipped Obey profile also answers to **Obi**, **Obie**, and **Oh bay**.
These are explicit alternate addresses, not a broader fuzzy matcher: ordinary
words such as "okay" and "obituary" do not become persona selections. A nickname
shared by two personas is ambiguous; use a unique name or select from the UI.

Existing Obey profiles keep their saved aliases. To add the same alternatives,
edit `personas/obey/persona.yaml` under your config directory and add or extend:

```yaml
aliases:
  - Obi
  - Obie
  - Oh bay
```

Use the spelling shown in the transcript when adding another observed
misrecognition. Avoid common acknowledgements such as "okay" as aliases. The
speech correction layer intentionally does not fuzzy-match short, common names
like Obey. You can also change the spoken name in **Personas**, or with
`obey-voice persona edit obey --display-name Obi`; the storage ID and saved
conversation labels remain separate from the display name.

### Shared conversations

Personas can share a conversation across brain providers. Each keeps its own
prompt, model, and voice while receiving context from the shared thread.

With a supported local Ollama persona, address another configured persona to
select it: `Santa.` gives a short acknowledgement in Santa's voice; `Santa,
what do you think?` selects Santa and answers the request. Santa remains the
responder for ordinary follow-ups without another name, including after quiet
gaps, microphone pause/resume or editing. Use the Talk persona control or TUI
`/personas` for explicit selection. `/speakers` still manages human speaker
labels; it does not choose the responding persona.

Each responder receives context identifying this as the current Obey Voice
conversation with you and the other personas. The prompt distinguishes agents
whose replies are recorded in the shared thread from personas merely available
to join. Switching personas retains the conversation; it does not ask the new
responder to set up a different chat. Other agents' statements keep their own
authorship, and an agent should respond only as itself. This context is supplied
to Ollama and to new or resumed Claude, Cursor, Grok, and Codex sessions.

In the TUI, type a leading `@` to list enabled personas. Type part of a name,
ID, or alias to fuzzy-filter the list; use Up/Down or Shift+Tab to choose and
Tab to insert its canonical `@id`. Send `@joker What do you think?` to select
Joker and deliver the question directly, without model-based addressee
classification. A bare `@joker` selects that persona with an acknowledgement.
Unknown IDs are refused; mentions in ordinary prose and email addresses do
not select a persona.

Use `/menu` (or `/back`) to save the current chat and return to the launcher
inside the same terminal UI. The app drains the conversation runtime before
enabling the launcher; Continue offers the saved chat.

Obey Voice capability guidance is added to the current request when it asks
about this app's capabilities or configuration, or directly requests document
conversion to audio. It is absent from persistent system prompts and unrelated
requests, including requests to implement conversion in another project. The
current input selects the guidance; earlier app questions do not keep it active
across topic changes. Relevant requests receive core capability facts even when
skills are disabled. Those facts include the current Calibre setting and require
readiness checks before claiming a feature can execute.
The embedded `obey-voice` skill supplies detailed app workflows. Listed group
participants already exist; asking one to speak does not request creating it.
Peer replies have tools disabled and their discussion of commands cannot
activate the legacy spoken-promise worker fallback. They still use the normal
speech output path.

Application and conversation instructions use the same versioned YAML format
as persona/turn prompts. Packaged sources live under
`pkg/voiceagent/prompts/defaults/system*.yaml`; startup seeds editable copies
under `<prompts_dir>/system/` without overwriting existing edits. Use
`obey-voice prompts list` and
`obey-voice prompts show system --name application` to inspect them. Named
system documents are shown as templates, with runtime placeholders intact.

`application.yaml` groups app capabilities into named sections. `group-chat`,
`group-available`, `group-spoken`, `group-reply`, `peer-reply`, and
`conversation-history` own conversation context; `addressee` and `acoustic-name`
own private routing instructions. Metadata in each file names its runtime
variables. Go supplies values such as the current persona, public roster and
Calibre enablement. Edits take effect on the next relevant request; invalid YAML or
unknown placeholders report a warning and use the packaged YAML. Unchanged
templates produce identical context, and prompt text stays out of canonical
conversation history.

Exact persona controls are intentionally narrow and whole-input only:

- direct name/alias (`Santa`, `Santa.`)
- one greeting filler plus name/alias (`Hey Santa`)
- explicit switch form (`switch to Santa`)

Quoted names (for example `"Santa"`), multi-target phrases (`Santa and Jarvis`)
and substantive tails (`switch to Santa and tell me a story`) stay semantic and
use normal addressee inference.

A leading punctuated address such as `Joker, ask Venessa what she thinks`
also selects the named listener directly and preserves the full request. Joker
takes the first turn and can then invite Venessa to answer. Instructions to
“you and Venessa” to keep talking go to the current responder first.

Incidental mentions such as `Santa is a persona` do not select Santa. Semantic
routing can select only a configured agent name or alias; human names and topic
names such as LFM3 stay with the current responder. Unknown exact `@id` or
`switch to Name` controls, ambiguous agent names and failed prompt/model/voice preparation
leave the old responder selected and report an error. A shared alias or generated
short name (for example, `Veronica` and `Veronica Hype`) does not block ordinary
follow-ups or requests addressed to another persona. Use a unique full name or
`/personas` when a spoken name is ambiguous. Response errors after speech was
recognized keep voice input enabled; only repeated input failures switch to text.
Classifier routing failures now add a manual-retry notice in chat/activity and
recordings: resubmit the request as new input, or use exact persona control first
(`switch to Santa`, then the question). The original user input remains visible
in the current session. Obey Voice does not auto-replay failed inputs and does
not keep a failed-turn retry queue. `persona_routing_timeout` defaults to 120
seconds and accepts 1–300 seconds. Set it with
`obey-voice config set persona_routing_timeout 90` for future conversations. This
includes model queueing/loading as well as classification; a cold or busy model
can exceed the former fixed 15-second limit before generating a decision. A
shorter parent deadline or cancellation still stops the request immediately.
Shared-history projection
is verified for Ollama, Codex, Muse Spark, and Obey personas: Codex is tested
on its own, and Muse Spark and Obey each share a conversation with an Ollama
persona. A persona backed by Claude Code can also be switched to and back,
including by an `@` handoff from another agent; this is tested with fake
Ollama and Claude backends, not with live models. Switching to a persona on Grok
or Cursor is refused rather than silently replaced. The local acceptance uses
Qwen3:8b and Kokoro voices `af_heart` and `am_michael`; it does not establish
parity for every provider.

A conversation can still start on a Claude, Grok, or Cursor persona, with the
same persona controls. Those providers keep their own session history without
speaker identity, so Obey Voice keeps the shared transcript itself and records
each reply, including an interrupted one, under the persona that gave it.
Selecting an Ollama, Codex, Muse Spark, or Obey persona from there hands it that
attributed transcript. Clearing the conversation or changing its settings
restarts the same persona's provider session from the transcript. Group
participation is not offered while one of these personas is responding.

A persona backed by Claude Code keeps its Claude session while other personas
speak: Obey Voice parks it and resumes the same session when that persona is
addressed again. Each request carries, as quoted conversation records, only the
lines that session has not seen: your lines, other personas' replies, and
replies Obey Voice recorded under that persona's name. Other personas' tool
calls and tool output are not sent. Lines count as seen only
after a reply completes, so an interrupted reply is sent again next time, marked
as unfinished. Claude receives nothing said before its persona was first
addressed in the conversation; when another agent hands it the floor, that
agent's message is the first line it receives. When the unseen lines do not fit
one request (64 KiB, or half of `claude_max_session_tokens` when that fuse is
set), Obey Voice starts a fresh Claude session and sends the newest lines that
fit, with a note of how many earlier lines were left out. At most four sessions
stay parked; the least recently used one is closed first. A parked session is
also closed when its persona's prompt, model, provider or other settings change,
when the conversation is cleared, and when Obey Voice exits. After resuming a
saved conversation, a persona on Claude starts a new session that receives the
lines since it was first addressed, within the same limit. With group
participation on, a persona on Claude can be addressed directly, but a
discussion among several personas still requires every member to be on Ollama.

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
In a conversation that started on Grok or Cursor, the summary reseeds only that
provider's session; the saved transcript stays complete. For a persona on
Claude, compaction ends its Claude session, and the next request starts a fresh
session from the transcript lines described above rather than from the summary.
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

## Starting directories

A persona may name its own working folder within a shared conversation: file
and shell tools, worker boundaries, skills and campaign context all use it.
Conversation history stays shared across all participating personas. Empty
inherits the application working directory, which is what shipped personas do.

In the TUI form the Starting directory row cycles with `←`/`→` through
`(default)`, every registered local camp (discovered with `camp list --json`
when the `camp` CLI is installed, exactly as Settings → Meeting finds route
destinations), and `(custom)`, which opens a text field for any folder (`~` is
expanded). The line under the row shows the folder the choice resolves to. The
folder is checked when the persona is saved, before anything is written: a
missing folder or a file keeps the form open on the row and names the persona
and path. A persona whose saved folder has since disappeared still opens and
saves its other fields; the row says the folder is missing until `(default)` or
another folder is chosen. When camp discovery fails or finds nothing, the row
says so and `(custom)` stays available. The Personas list detail pane shows
each persona's configured directory or `(app default)`.

From the shell, `obey-voice persona edit samantha --starting-directory ~/Dev/api`
saves the same value; `--starting-directory ""` restores inheritance. Create
accepts the same flag. List/show JSON includes `starting_directory`.
`persona directories --json` lists the registered local camps the form offers;
discovery failures return a notice without disabling custom paths.

For a new conversation, explicit `--work-dir` / desktop API override wins,
then `OBEY_VOICE_WORK_DIR` (legacy `WORK_DIR`), then the persona, then the app
working-directory resolver. **Obey (default)** honors Obey's directory too. The
app default is retained separately from each persona’s resolved directory.
Switching or addressing another responder uses that persona’s own directory
for tools, project context, skills and background work. A persona set to
`(default)` uses the original app directory, never the previous responder’s camp.

Each persona’s canonical path and source are saved when it first participates
in the conversation. Resume restores these choices; clearing history, editing
settings or editing a profile does not move an already-bound persona. Start a
new conversation to use an edited directory. Legacy sessions retain their saved
directory for the saved responder; if that directory came from a persona, other
responders inherit the current launch default until their own directory is bound.
Remote paths refer to the engine host. A remote provider without shared
persona selection cannot resume into a different directory in the same
running server; it reports an error before changing the active conversation.

A chosen directory must exist, be readable/searchable and not be filesystem
root. Invalid choices fail before starting tools. A missing saved path blocks
resume. Moving or unregistering a camp does not automatically redirect a
persona: select its new local path. This is a tool starting location, not a
filesystem sandbox or permission grant.

### Preview from the persona list

In the TUI **Personas** menu, highlight a persona and press **p** to hear its
voice. Press **p** again to stop. Moving to another row, editing, switching,
or leaving the menu stops the sample. The create row does not play audio.

In the desktop **Personas** list, click **Preview voice** on a card, then
**Stop preview** to cancel. Starting another preview stops the previous one.
These previews use the saved persona's speech provider, voice, and tier,
including inherited Settings defaults. They do not activate, edit, or save the
persona, and inactive personas can be previewed too.

## Group participation (experimental)

With a local Ollama brain, enable `group_participation_enabled: true` in your
configuration and start a new conversation. The default is disabled. Once enabled,
requests such as “What does everyone think?” or “Jarvis and Samantha, discuss this”
use semantic audience inference and offer the invited personas their own private
chance to contribute. An agent can pass. Later replies can answer or challenge a
specific earlier contribution, including returning to an earlier speaker.

Use `/participation` in the conversation to inspect settings, or
`/participation invited jarvis,samantha` to choose a subset. `all` includes enabled
personas. Modes are `off`, `invited`, and `open`; open permits at most one useful
addition after an ordinary completed reply. The desktop conversation and native
Mac Talk view expose the same controls. Settings belong to the current saved
conversation; resuming it does not resume autonomous speech.

From another terminal, `obey-voice participation show` and
`obey-voice participation set invited --members jarvis,samantha` control a running
local TUI/desktop conversation through its authenticated control endpoint. Use the
serve WebSocket control for a served conversation.

Discussions support up to eight invited personas. Replies are brief, tools and
workers are disabled for discussion contributions, and response, inference and time
budgets end the exchange. New input, interruption, persona changes and conversation
changes supersede pending contributions. There is no continuous idle polling.
Private decisions stay out of the transcript. Voice continuations require audio
accepted by an output sink; text conversations use completed text delivery.

An optional `participation_description` in `persona.yaml` describes when a persona
may have a useful perspective (at most 256 characters). Set it with
`obey-voice persona edit <id> --participation-description "..."`. This short card
helps candidate selection; each candidate still uses its own full persona prompt
and model to decide whether to contribute and generate its reply.
