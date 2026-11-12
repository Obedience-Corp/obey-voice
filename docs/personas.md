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
is verified for Ollama, Codex, Muse Spark, and Obey personas: Codex, Muse Spark
and Obey each share a conversation with an Ollama persona, and Codex also with
another persona on Codex. A persona on Claude can also be switched to and back.
These are tested with fake backends, not with live models. Switching to a
persona on Grok or Cursor is refused rather than silently replaced. The local
acceptance uses Qwen3:8b and Kokoro voices `af_heart` and `am_michael`; it does
not establish parity for every provider.

A conversation can still start on a persona on Claude, Grok, or Cursor. Those
providers keep their own session history without speaker identity, so Obey
Voice keeps the shared transcript itself and records each reply, including an
interrupted one, under the persona that gave it.

If no Ollama, Codex, Muse Spark, or Obey persona is enabled, such a
conversation stays with its persona: every input, including the persona's name
alone or another persona's name, goes to the model, and its prompts carry no
persona roster. On Claude, `switch to Name`, `@id` and `/personas` still hand it
to another persona on Claude, where it stays the same way; replies never hand
it off.

Otherwise a persona on Claude takes part like any other: you address personas
by name from it and to it, and its replies hand the floor to another persona
with an `@` mention. A conversation on Grok or Cursor cannot come back once
another persona takes it over, so it moves only when you ask explicitly with
`switch to Name`, `@id`, or `/personas`; addressing another persona by name is
refused with that hint, and its replies never hand the conversation off.
Selecting a persona on Ollama hands it the attributed transcript. A persona on
Codex, Muse Spark or Obey keeps that transcript too, but is sent only its share
of it (see below).

Voice and audio settings changes keep the provider session. Clearing the
conversation, or changing the persona's provider, model, prompt, name or
directory, restarts the session from the transcript: the provider's own
session is dropped. A new Grok or Cursor session starts with the transcript's
last `max_history` turns; a new Claude session receives the persona's share as
described below. Group participation is not offered while one of these
personas is responding.

A persona on Claude keeps its Claude session while other personas speak: Obey
Voice parks it and resumes the same session when that persona is addressed
again. Each request carries, as quoted conversation records, only the lines
that session has not seen: your lines, other personas' replies, and replies
Obey Voice recorded under that persona's name. Other personas' tool calls, tool
output and the names of attached files are not sent. Lines count as seen only
after a reply completes, so an interrupted reply is sent again next time, marked
as unfinished, even if Claude reports success after it was stopped. A stopped
reply finishes in the background, and its session is not used again until it
has, unless your next line would wait more than five seconds for it: then that
line goes ahead, the stopped reply keeps only what you heard, and nothing it
sends after that is recorded or counted as seen. A parked session is closed
only once its stopped reply has finished, or after five seconds.

An image you attach while a persona on Claude is answering is sent to Claude
with that line. Claude takes at most 7.5 MiB per image as sent (a GIF as the PNG
of its first frame), below the 8 MiB the attachment store allows, so a larger
image is refused before the request. If the service still refuses an image,
the turn fails saying so and the session is kept: the line is not retried in a
new session, which would lose the conversation and fail the same way. A resumed
session already holds an image, so later requests carry only new images. A fresh session for the persona, after
the session fuse, a catch-up that does not fit or a rejected resume, is sent the
images of the persona's own earlier lines again where they fit: the newest
first, within 20 images and 32 MiB together with the new line's images, which
come first. An older image past those limits, one larger than Claude takes, or
one no longer in the attachment store, is left out and named as no longer
attached, so only the new line's images can make a request fail. The images of
a line whose reply failed or was stopped are not replayed, and a compaction
summary request is sent none. An image attached to a line said to another
persona never reaches a persona on Claude, even with `hosted_persona_sharing`
set to `whole_conversation`: the line reaches it as text, without the image or
its file name. Only the lines the persona's running Claude provider answered
itself keep their images: the provider drops the images of every line loaded
into it, so after a resume or a compaction, or once Obey Voice closes the
persona's parked session or starts its provider again, its earlier lines reach
it as text and their images are not sent again. The saved conversation keeps
every attachment. A background job on a Claude worker is sent the images of the
line that started it, whichever persona or backend answered that line, as a
Codex worker is: the worker provider is one you configured, and it gives no
hosted notice.

Claude receives nothing said before its persona was first addressed while on
Claude in the conversation, unless `hosted_persona_sharing` is set to
`whole_conversation` (see [Hosted backends](#hosted-backends)). Lines from
before that point are not sent,
including the persona's own replies from when it ran on another backend. When
another agent hands it the floor, that agent's message is the first line it
receives. The one exception is a conversation saved before replies named their
persona: when it is resumed with the persona it was saved with, on the backend
its session was created on, and no reply in it names a persona, that persona
receives it from the start. The point is saved with the conversation and still
applies after a resume, including when serve resumes another conversation and
that fails.

A line longer than half of one request's limit (64 KiB, or half of
`claude_max_session_tokens` when that fuse is set) is shortened, and its record
says how much was left out. When the unseen lines still do not fit, Obey Voice
starts a fresh Claude session and sends the newest lines that fit, with a note
of how many earlier lines were left out. A persona on Claude receives the
pending results of background jobs it started, whichever backend it was on when
it started them: a job it started on another backend, local or hosted, before
it was moved onto Claude has its task and result sent to Claude when they are
delivered. Results of jobs other personas started wait for those personas. A
job saved before jobs recorded their persona goes to the persona answering when
no other persona is in the conversation, and an unset `active_persona` counts
as the default persona.

A fresh Claude session therefore starts with a replay of up to 64 KiB of the
persona's share, where it used to receive only the last six lines. This
applies to a conversation with a single persona on Claude too: the first
request after a resume, after the session fuse resets the session, or when the
unseen lines do not fit, sends more input tokens and costs more than before.
After a compaction the fresh session starts from the summary instead. A prompt
of 100 KiB or more reaches the Claude CLI on stdin rather than as one
command-line argument.

At most four sessions stay parked; the least recently used one is closed first.
A parked session is also closed when its persona's prompt, model, provider or
other settings change, when the conversation is cleared, and when Obey Voice
exits. After resuming a saved conversation, a persona on Claude starts a new
session that receives the lines since that point, within the same limit. With
group participation on, a persona on Claude can be addressed directly, but a
discussion among several personas still requires every member to be on Ollama.

A persona on Codex, Muse Spark or Obey keeps the whole attributed conversation
on this machine, so the conversation is saved complete and a persona on Ollama
that answers next is handed all of it. What it sends its backend is only its
share, by the same rules as a persona on Claude: nothing said before it was
first addressed while on that backend unless `hosted_persona_sharing` is
`whole_conversation`; when another agent hands it the floor, that agent's
message is its first line; and the same exception applies to a conversation
saved before replies named their persona. Lines said while it was away are added
to its share when it is addressed again. Each request replays the share as
quoted conversation records (Codex replays it when it starts a thread, then
continues that thread): your lines, other personas' spoken lines, and the
persona's own lines and replies. Other personas' tool calls, tool ids and tool
output, and the names and images of files attached to any line the persona did
not record itself, are not sent. Lines it recorded while answering on that
backend are kept as recorded, so Codex still receives an image you attached for
it when it starts a new thread. The list of personas that have spoken, which
these backends receive in a shared conversation, names only those that spoke
within the share, including those whose lines compaction or `max_history` has
since dropped from what is replayed. The share start is saved with the
conversation and still applies after a resume. It starts again when the persona
is moved to another backend, so a persona moved from a local backend sends
none of its local turns; the task and result of a job it started there are
still sent when the result is delivered. Compaction summarizes only the share,
and the exchange kept after the summary is the last one in the share. Like a
persona on Claude, it receives the pending results of background jobs it
started, on whichever backend it started them, and not those of other personas.
A job saved before jobs recorded their persona, or started outside a spawn,
goes to it when no other persona is in the conversation, even one it started
on a local backend before it was moved.
Its hosted notice follows its share as for Claude: in a conversation it has to
itself it reads "What is said to it is sent to Codex." (or Muse Spark, or Obey),
not "This conversation is sent to Codex."

A conversation saved by an earlier version of Obey Voice has no share start for
these personas, and their replies name their persona, so the exception for
older conversations does not apply: after such a resume, a persona on Codex,
Muse Spark or Obey is sent nothing said before the resume, even in a
conversation it had to itself, unless `hosted_persona_sharing` is
`whole_conversation`.

### Hosted backends

A persona's backend is hosted when it sends conversation text to a service
outside this machine. Claude, Codex, Cursor, Grok and Muse Spark are hosted.
Obey is treated as hosted too: it passes the conversation to whichever provider
its daemon is configured with, which Obey Voice cannot see. Only Ollama is
local. A provider that is not classified is treated as hosted. The
classification covers a persona's conversation backend. It does not cover its
worker provider, and Ollama is local even when `ollama_host` points at another
machine.

Wherever personas are shown, a persona on a hosted backend is marked:

- the Personas list in the terminal UI shows `[hosted]` after the provider and
  model, and its detail pane says that conversation text leaves this machine;
- `obey-voice persona list` prints `[hosted]` after the brain and its `--json`
  rows carry `"hosted": true|false`; `GET /v1/personas` carries
  `brain.hosted`;
- the conversation header badge reads `brain claude · default · hosted`, and
  each reply from such a persona is headed `Name [hosted]`;
- persona selection events carry `hosted`, and the response envelope on every
  reply, speech and metrics event carries `hosted` and `brain_provider`. The
  desktop state carries `brain_hosted`, and each turn carries `hosted`.

Replies restored from a saved session do not record the backend they were given
on, so a resumed conversation marks new replies only; the header badge and the
Personas list still show the persona's backend as it is now.

The first time in a conversation that a persona on a hosted backend is about to
be sent anything, Obey Voice says so once, before the request: an info line in
the conversation and, in voice mode, the same sentence spoken. For a persona on
Claude:

> Mira runs on Claude, a hosted service. Only what is said from when it was
> invited is sent to Claude; earlier lines stay on this machine.

The second sentence follows what that persona's session will actually receive:
"The whole conversation is sent to Claude." when it starts from the beginning
(`whole_conversation`, or a resumed conversation saved before replies named
their persona), and "What is said to it is sent to Claude." when the
conversation is still empty. A persona on Codex, Muse Spark or Obey is
described the same way. A persona on Grok or Cursor, which only holds a
conversation that started on it, gets "This conversation is sent to Grok." The
notice is given once per conversation and provider, and once more if a persona
on that provider would receive the whole conversation after the user was told
it receives only what is said from its invitation (a later persona first
addressed under `whole_conversation`, for example). It also comes before
`/compact` when compaction is the first request to that provider. It is
forgotten when the conversation is cleared, and is saved with the session so a
resumed conversation does not repeat it (a session saved before this existed
repeats it once, and one saved before the whole-conversation case was recorded
gives that notice again when it applies). It counts as given only once it was
shown and spoken and the request went ahead: a turn stopped during the notice
gives it again next time, and a turn that sends the persona nothing (one
refused by its image check, or routed to a job or a story) does not give it.
It is shown by the host: no model receives it and it is not recorded as any
persona's reply. A bare `Mira here.` address sends
nothing and does not trigger it.

A conversation that started on the hosted provider itself, such as a Claude-only
install, gets the info line but no spoken notice the first time, so it does not
announce itself at every launch. A later notice for that provider, saying that a
persona on it now receives the whole conversation, is spoken as well. The marker
is still on every reply.

`hosted_persona_sharing` chooses what a persona on Claude, Codex, Muse Spark or
Obey receives of a conversation shared with other personas:

| Value | Meaning |
|-------|---------|
| `from_invite` (default) | Only what is said from the point the persona is first addressed while on its backend. |
| `whole_conversation` | The spoken lines of the whole conversation, from its start. |

Under both, other personas' tool calls, tool ids, tool output and attached file
names are never sent, and the persona receives only the results of jobs it
started, on any backend. Asked what is happening, such a persona names only the
jobs it started, with their progress; any other running or queued job is only
counted ("One other job is running."). That answer is
recorded as the persona's reply and sent with its share, so it never carries
another job's task or tool activity. On Claude, whole-conversation catch-up is
bounded like any other: the line and request limits above still apply. On
Codex, Muse Spark and Obey the share is replayed within `max_history`, as their
history always was. The setting is read when a persona is first addressed on
its backend; selecting it from a list and then switching to another persona
without speaking to it does not count. Changing it mid-conversation does not
send earlier lines to a persona whose share has already started; it applies to
personas first addressed after the change. A persona on Grok or Cursor can only
hold a conversation that started on it, and is sent all of it.

The notice covers a persona's conversation backend only. Two other paths can
send conversation text off this machine without it: a background job on a
hosted worker provider, and [decision routing](decision-routing.md) when set to
Jev or OpenAI. Both are services you configure yourself, so neither gives a
notice.

The brief a new background job is given carries up to eight recent spoken
lines to the worker provider: with a persona on Claude, Codex, Muse Spark or
Obey answering, only lines from that persona's share.

Known gaps: a reply from a background worker started through the older worker
path is recorded as a spoken line of the persona that started it. Like any
spoken line, it is sent to a persona on Claude, Codex, Muse Spark or Obey whose
share includes it, including the one that is live when it arrives. The optional
work-decision service, when one is configured, is sent the live provider's
conversation while it is at most eight turns long; with a persona on Codex, Muse
Spark or Obey live, that is the whole conversation, including other personas'
tool output. A persona selected from a list while `whole_conversation` is set
is sent the whole conversation if the setting is narrowed while it is still the
one answering, before anything is said to it; the change takes effect for it
only after another persona is selected in between.

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
Claude, the summary is taken from its live Claude session, which also receives
the lines it had not seen (shortened or left out with a note when they do not
fit), and the next request starts a fresh Claude session from the summary
followed by only the lines said after it. For a persona on Codex, Muse Spark or
Obey, only its share is summarized and the saved transcript stays complete. As
with the other providers' saved history, the transcript keeps every turn, in
memory and on disk, until the conversation is cleared.
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
