# Personas (voice agents)

[Back to README](../README.md)

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
(Brain, Model, TTS, Voice, Language, Tier), the Starting directory, and the
System prompt. `↑`/`↓` (or tab) move between rows, `←`/`→` change the provider,
model, voice, language or directory under the cursor, `p`
on the Voice row previews the selected voice (the same sample Settings uses;
`(default)` plays the inherited Settings voice), and `ctrl+j` (also `alt+s`,
`f2`, `ctrl+s`) saves from anywhere — so changing a persona's model or voice
never goes through the prompt editor. `esc` cancels, except while the prompt
differs from what the form opened with; that is discarded explicitly with `:q`.

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

### Shipped personas

| Persona | Character | Speech provider | Voice |
|---------|-----------|-----------------|-------|
| Obey | Direct, practical assistant | `openai-speech` (Phonon) | `Harper` |
| Samantha | Warm, curious conversational companion | `openai-speech` (Phonon) | `Juniper` |
| Jarvis | Precise, dry British assistant | `openai-speech` (Phonon) | `Edwin` |
| Veronica | Playful conversational companion | `openai-speech` (Phonon) | `Zoey` |
| Santa | Warm storyteller with a practical workshop streak | `kokoro` | `am_santa` |

All five inherit the application brain unless you set one on the profile.
The named Obey profile pins Harper; **Use defaults** still takes its brain and
speech settings from Settings, including a different engine or voice you chose.
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

With a supported local Ollama persona, address another configured persona to
select it: `Santa.` gives a short acknowledgement in Santa's voice; `Santa,
what do you think?` selects Santa and answers the request. Santa remains the
responder for ordinary follow-ups without another name, including after quiet
gaps, microphone pause/resume or editing. Use the Talk persona control or TUI
`/personas` for explicit selection. `/speakers` still manages human speaker
labels; it does not choose the responding persona.

Exact persona controls are intentionally narrow and whole-input only:

- direct name/alias (`Santa`, `Santa.`)
- one greeting filler plus name/alias (`Hey Santa`)
- explicit switch form (`switch to Santa`)

Quoted names (for example `"Santa"`), multi-target phrases (`Santa and Jarvis`)
and substantive tails (`switch to Santa and tell me a story`) stay semantic and
use normal addressee inference.

Incidental mentions such as `Santa is a persona` do not select Santa. Unknown
explicit targets, ambiguous addressed names and failed prompt/model/voice preparation
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
working-directory resolver. “Use defaults” skips the persona override. The
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
