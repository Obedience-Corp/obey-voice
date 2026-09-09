# Meetings and speaker labels

[Back to README](../README.md)

`obey-voice meeting record` listens continuously (STT only — no Brain, no TTS)
and creates one timestamped `.meeting` bundle directory. Its canonical
`meeting.md` and internal structured event stream are each synced so a crash
never loses what was already captured.

From the main `obey-voice` launcher choose **Meeting → Start meeting**, or run
`obey-voice meeting record` on a TTY (not `--json` / `--no-tui`) for the
full-screen recorder. **Resume last meeting** (or `obey-voice meeting record
--resume last`) appends to the same `.meeting` bundle: timestamps continue
from the last line, the break is marked `▶ resumed`, and retained `audio.wav`
is extended rather than replaced. `last` means the **newest meeting by start
time**, not the one most recently written to. The reported duration counts
recorded minutes only, so a meeting split across a two-hour break still reads
as the 40 minutes it captured. A meeting that already routed its notes is not
auto-filed a second time — in the launcher and on the command line alike;
every other resume follows `meeting.route.mode` exactly like a fresh
recording, and `obey-voice meeting route` refiles on demand.

| Control | Action |
|---------|--------|
| Type + **Enter** | Save a note at the current timestamp |
| **Ctrl+B** | Mark this moment ★ important (optional caption from the note field) |
| **Ctrl+C** / **Ctrl+Q** | Stop recording |
| Spoken stop phrase | "stop recording" / "end meeting" / "stop listening" (exact utterance; not written to the meeting) |

Spoken stop phrases end the session like Ctrl+C and are **not** appended to the
meeting transcript. Meeting bundles and internal directories are created mode
`0700`; documents and machine data are `0600` (owner-only).

Meeting speaker analysis has two layers (both **on by default**):

1. **Live provisional labels while recording** (**Settings → Meeting → Live
   labels while recording**, key `speaker.meeting.live`) — the same embedding
   path as chat. Finalized STT lines get a colored `speaker-N` glyph (or 🎤
   when unknown). Labels can revise on the latest line as the engine catches
   up; brief empty gaps hold the last good label for a few seconds. A footer
   line marks these as **provisional**.
2. **Offline diarization on stop** (**Settings → Meeting → Speaker
   diarization**) — the review-screen source of truth. Stopping opens review
   immediately (the REC timer freezes at stop) and diarization continues **in
   the background** — in the launcher and in the standalone
   `obey-voice meeting record` TUI alike; the attributed `speaker-1…N` turns
   fold into the review when it finishes, or a status line reports the
   failure.

Live labels and offline results may disagree; that is expected. The live
scrollback is left as-shown; review uses the offline timeline. The first
session that needs models installs Obey Voice's managed packs (embedding for
live; pyannote segmentation + NeMo TitaNet for offline), then captures 16 kHz
PCM through a non-blocking subscriber so STT is never blocked.

**Expected speakers** (**Settings → Meeting → Expected speakers**, key
`speaker.meeting.num_speakers`) applies to **offline clustering only**: cycle
**Auto** (0) or a fixed count (**2 / 3 / 4**). Auto can **over-split** long
1:1 interviews; set **2** for two-person calls. These are voice clusters, not
enrolled names or identity claims. Each label keeps a stable color on review
and on live chat/meeting rows (shared palette). Continue from the review
screen to the configured routing flow. Turn either layer off in Settings if
you do not want it.

**Diarization engine** (**Settings → Meeting → Diarization engine**, key
`speaker.meeting.diarizer`) chooses which model runs that offline pass. The default `sherpa` is the managed pyannote + NeMo
TitaNet path described above and is what every install gets. `sortformer`
swaps that one step for NVIDIA Streaming Sortformer through the
[transcribe.cpp](https://github.com/handy-computer/transcribe.cpp) CLI
(`transcribecpp_binary`) plus a managed 139 MB GGUF that `models ensure`
installs once the key is set; it is optional, and everything else — live
labels, embeddings, enrolled names — stays on sherpa either way. It applies to
every path that produces a review: `meeting record`, `meeting analyze`, and
meetings finished through `serve`.

Sortformer resolves **up to four** speakers and has **no speaker-count input**,
so **Expected speakers** has no effect while it is selected — not on the model,
and not on the sparse-label merge that the count gates under sherpa (a setting
that reached nothing must not go on relabelling the result). The **Expected
speakers** row and `obey-voice doctor` both say so rather than pretending it
applied. Switch back to `sherpa` and
the count applies again. A missing CLI or GGUF fails at meeting start, not
after the recording. An unrecognised value is a config error: the engine that
labels a meeting never changes by typo.

Background diarization never holds the transcript hostage: in the launcher,
quitting Obey Voice cancels the enrichment only; in the CLI, leaving review
while analysis is still running prints `Diarizing speakers…` and a further
**Ctrl+C** skips the labels (the transcript and any delivered route are
already on disk). `--json` waits for the labels so the summary keeps its
speaker fields — Ctrl+C skips there too. Re-running diarization on a past
meeting with `obey-voice meeting analyze` needs retained audio
(`speaker.meeting.record_audio` / **Record audio for analysis**); without it
the working PCM is discarded after Finalize.

That discard decides how a **resumed** meeting is labelled. With
`record_audio: true` the whole recording is still on disk, so each stop
diarizes the meeting end to end and every line is labelled from the same
pass. With retention off — the default — the audio of earlier segments is
gone, so a resumed stop diarizes **only the segment it just recorded**: it
labels that segment's lines and leaves the labels the earlier stop already
wrote exactly as they were. Nothing is relabelled from audio that no longer
exists, and no line is dropped.

The sidecar `speaker-analysis.json` is overwritten by each pass, so it
describes the most recent one only; the bundle's event stream is the full
record.

The cost is that speaker numbering need not line up across the break: the
`speaker-2` of the second segment is not necessarily the `speaker-2` of the
first, because clustering starts fresh on each pass. Enrolled voices
(**Settings → Speakers**) resolve to names and do stay consistent. Turn on
**Record audio for analysis** if you want one coherent numbering across a
resumed meeting.

A campaign/file destination chosen at meeting start is **durable**: the plan
is written into the bundle and the route fires as soon as capture stops —
review does not gate it, and Ctrl+C on the review screen continues instead of
quitting. If the route cannot complete (quit, crash, or failure), the outcome
is recorded on the bundle (`routed` / `route_failed`) and the next launcher
start retries undelivered plans automatically (bounded attempts);
`obey-voice meeting route <bundle> --to <dest>` remains the manual recovery.
The routed note carries the transcript as it stood at capture end; speaker
labels that finish later live in the local bundle.

## Speaker labels in chat

Labeling **chat** turns is the same live embedding path (**Settings →
Speakers → Speaker labels in chat**), also **on by default**. A voice
conversation prefixes each user bubble with `speaker-1…N` and colors the
bubble border to match, and the footer shows the current speaker. Chat does
not run offline diarization.

The Speakers tab tunes the two levers that decide live label quality (chat
and meeting live share these):

| Row | Key | Effect |
|---|---|---|
| Match threshold | `speaker.live.threshold` | Lower merges similar voices onto one label; higher splits a voice into a fresh `speaker-N` more readily |
| Analysis window | `speaker.live.window_ms` | Shorter labels a turn sooner; longer gives the embedder more voice to work with |

Labels are enrollment-free: the first sight of a voice registers `speaker-N` and
later turns re-match against it. Live analysis only needs the embedding model;
offline meeting diarization additionally needs the pyannote segmentation pack.

In an open conversation, `/speakers on|off|status` toggles labeling for that
session and loads the model on first use — no restart. Settings persists the
choice for future conversations. Live analysis needs microphone capture, so it
is unavailable in `--text` mode.

When labels are active, the **model** also sees speaker attribution on user
turns (`speaker-1: …` in the prompt, or a human name after rename or Settings).
**Settings → Speakers → Your name** (`user_display_name`) is the persistent
desk default: a one-person session overlays that name onto `speaker-1` so the
model sees `Lance:` instead of `speaker-1:`. It does not enroll a voiceprint.
A second live cluster stays `speaker-2` until named or enrolled.

Session rename (does not write config):

```text
/speakers name 1 Lance
/speakers name speaker-2 Alex
/speakers names
```

Renames are session-local: they update bubbles, the footer, and subsequent
prompt attribution, and they win over the Settings name for that chat. They do
not change the embedding manager’s stable `speaker-N` ids. The model cannot
rename speakers; only this command does.

### Register your voice

**Settings → Speakers → Register your voice** records three short clips on the
conversation’s own microphone — the same capture graph live analysis listens
to — and stores a voiceprint under **Your name**. The microphone is live during
the recording even though entering Settings mutes the conversation; `Esc`
cancels and nothing is saved. Registering while a chat is open takes effect
immediately: the running engine is seeded, so the next turn that matches is
labelled with the name instead of `speaker-1`. The first registered profile is
the machine’s **owner** — the one Settings calls “your voice”, the profile
**Re-enroll your voice** replaces in place and **Remove your voice** deletes
(chat falls back to `speaker-1` until you register again; a conversation already
running keeps the old voice until it restarts). The flag picks that subject, not
an access level: `speaker.live.mode=owner_verify` and
`barge_in_speakers=enrolled_only` accept any enrolled profile, including guests.

**Enroll another speaker…** appears once anyone is in the store. It asks for a
name, records the same three clips, and stores a **guest** — it never claims
ownership, even if the owner was removed. Guest rows list every non-owner
profile (including voices enrolled from the CLI or the Mac app). Enter
re-enrolls that name; `d` then `y` removes it. Transcripts already written keep
the name; an open conversation keeps the old live label until it restarts.
`/speakers name` is still session-local and is not a voiceprint.

Changing `speaker.live.window_ms` restamps the enrollment recipe: embeddings
recorded at the old window are no longer comparable, so the row says
*(re-enroll to refresh)*, `obey-voice speaker list` marks the profile, and live
seeding skips it until it is recorded again. The Speakers tab warns when you
cycle the window while voices are enrolled.

The same store is used by the CLI, so a clip recorded elsewhere works too:

```bash
obey-voice speaker enroll --name Lance --from-wav clip.wav
obey-voice speaker list     # * marks the owner
obey-voice speaker remove Lance
```

`obey-voice doctor` warns when chat labels are on and no voice is registered.

The meetings directory contains one visible item per recording:

```text
weekly-planning-20260722-090000.meeting/
  meeting.md
  audio.wav                 # optional
  .samantha/
    events.jsonl
    speaker-analysis.json
```

The attributed transcript is added to `meeting.md`, and routed meeting notes
prefer those attributed utterances. Enable **Record audio for analysis** only
when a private `audio.wav` is also desired. Model or analysis failures are
shown and logged without discarding or stopping the meeting transcript.

JSONL events include `offset_ms` from meeting start for alignment:

```json
{"type":"utterance","ts":"...","offset_ms":12340,"text":"next agenda item"}
{"type":"note","ts":"...","offset_ms":15000,"text":"follow up with finance"}
{"type":"bookmark","ts":"...","offset_ms":18200,"label":"important","text":"budget decision"}
{"type":"speaker_analysis","ts":"...","offset_ms":20000,"status":"complete","speaker_count":2,"artifact":"...speaker-analysis.json"}
{"type":"speaker_utterance","ts":"...","offset_ms":12340,"id":"utterance-1","text":"next agenda item","label":"speaker-1","start_ms":11000,"end_ms":12340,"state":"stable","timing":"estimated"}
```

```bash
obey-voice meeting record
obey-voice meeting record --description "Weekly planning sync"
obey-voice meeting record --description "Standup" --out-dir ~/notes/meetings --json
obey-voice meeting record --description "CI log" --no-tui
obey-voice meeting record --resume last
obey-voice meeting analyze meeting.wav --speakers 2
```

Bundles default to `~/.obey/agents/voice/festival-voice/meetings/<slug>-<timestamp>.meeting/`.
Meeting routing accepts a `.meeting` bundle or its canonical `meeting.md`.
`--json` also emits one JSON line per utterance plus a final summary on stdout.

## Campaign routing (camp CI0009)

When launched inside a campaign (including a project or worktree beneath it),
Obey Voice uses that campaign as the meeting destination when
`meeting.route.default` is empty. Detection follows the resolved working
directory, so an explicit `work_dir` setting or `serve --work-dir` override
also changes the campaign default. This is a session default and is never
saved to your global settings.

In `ask` mode the campaign is preselected; in `auto` mode it receives the
meeting automatically. Explicit route choices and configured defaults take
precedence, and `off` / `--no-route` still keep notes local. Outside a campaign,
the existing routing behavior applies. The detected campaign does not need
to appear in `camp list`; delivery uses its local root directly.

When a meeting is routed to a **campaign** destination, Obey Voice defaults to
`capture: meeting` and runs:

```bash
camp idea notes import-meeting <bundle> --title "…" --summary-file … --json
```

inside that campaign’s root so the note lands under
`.campaign/intents/notes/meetings/` with a `.transcripts/` sidecar — **not** as
a lifecycle intent in Inbox/Ready/Active.

Legacy modes (opt-in in `meeting.route.destinations[].capture`):

| `capture` | Behavior |
|-----------|----------|
| `meeting` (default) | `import-meeting` → `notes/meetings/` |
| `intent` | `camp idea add` lifecycle intent (old, misfiling risk) |
| `note` | `camp idea add --note` |

Requires a camp build with CI0009 (`import-meeting`, #537/#539) on `PATH`.
