# Scenes: scripted two-persona conversations

[Back to README](../README.md)

`obey-voice scene` records a written conversation between two personas as audio.
Each persona brings its own voice, so the recording is a conversation rather
than one narrator reading both parts.

This is batch work: no microphone, no live pipeline, no interaction. It is not
`obey-voice render` (one mouth, one document) and it is not the live TUI.

```bash
obey-voice scene script.yaml --out-dir out/coffee
obey-voice scene script.yaml --out-dir out/coffee --pause 250ms
obey-voice scene script.yaml --out-dir out/coffee --resume --json
obey-voice scene script.yaml --out-dir out/coffee --audio-format mp3
obey-voice scene beats.yaml  --out-dir out/improv                  # generate mode: the personas write the lines
obey-voice scene beats.yaml  --out-dir out/improv --resolve-only   # write the lines, record nothing
```

`--out-dir` is required: a scene always writes several files.

## The script

A scene script is an `obey-voice.scene.v1` YAML document — a cast of exactly two
personas and either the turns they speak (`verbatim`) or the brief they
improvise from (`generate`). Unknown keys are a load error, so a typo surfaces
instead of being ignored.

```yaml
schema: obey-voice.scene.v1
id: coffee-counter
title: Coffee counter
mode: verbatim          # verbatim | generate; --mode wins only if you pass it
pause: 400ms            # optional; --pause wins only if you pass it
cast:
  - id: sam             # scene-local speaker id, referenced by turns
    persona: samantha   # an installed persona (obey-voice persona list)
  - id: uncle
    persona: uncle-fu
turns:
  - speaker: sam
    text: "You're late."
  - speaker: uncle
    text: "The bus had opinions."
```

Turns name **cast** ids, not persona ids, so recasting a part does not rewrite
every line. Both personas must be installed. A committed example lives at
[`testdata/scenes/two-voices.verbatim.yaml`](../testdata/scenes/two-voices.verbatim.yaml).

The full field list, including the `scene:` block generate mode reads, is in
[`docs/schemas/scene.schema.json`](schemas/scene.schema.json).

## What a run writes

```text
out/coffee/
  script.yaml            copy of the input
  script.resolved.yaml   the script as recorded: always verbatim, every line,
                         and each speaker's effective voice and brain
  turns/000-sam.wav      one WAV per turn, at the provider's native rate
  turns/001-uncle.wav
  conversation.wav       all turns joined, with the pause between them
  conversation.mp3       only with --audio-format
  manifest.json          obey-voice.scene-run.v1
```

`manifest.json` carries the effective stack for each speaker and, for every
turn, `speaker`, `text`, `wav`, `start_ms` and `duration_ms`. Those timestamps
are on the **combined** `conversation.wav` timeline, so a later captions or
video step does not have to re-derive them from the per-turn files:

```json
{
  "speaker": "sam",
  "text": "You're late.",
  "wav": "turns/000-sam.wav",
  "start_ms": 0,
  "duration_ms": 1820,
  "status": "complete"
}
```

Across turns that produced audio, `start_ms[i+1] == start_ms[i] +
duration_ms[i] + pause_ms` holds exactly, in whole milliseconds.

A turn that produced none occupies no time: a `failed` turn, and a `complete`
turn whose line cleaned away to nothing, both carry `duration_ms: 0` and an
empty `wav`. They are still placed — stamped at the instant the previous turn
ended — so a caption keyed on `start_ms` stays where the line belongs instead of
jumping to the start of the conversation. No pause is spent on them, so the
identity above skips over them to the next turn that spoke.

## Voices

Each persona speaks with its own TTS provider and voice. Personas that resolve
to the **same** voice — including one naming a voice the other inherits from
your app defaults — print a warning; `--require-distinct-voices` makes that an
error instead.

Two personas on the same engine share one worker, and each turn carries its own
voice. A cast mixing Kokoro and Qwen3-TTS loads both. Two **different** Qwen
tiers in one scene is refused: that would be two multi-gigabyte workers resident
at once.

Both parts on one engine that cannot take a per-turn voice is refused too, before
anything loads. Such an engine would read both parts in the same voice while the
cast still looked like two speakers, which would make `--require-distinct-voices`
pass on a one-voice recording. Kokoro and Qwen3-TTS both take a per-turn voice;
give one persona a different provider if you hit this.

WAV is always the source of truth. `--audio-format` encodes only
`conversation.wav`, never the per-turn files, and a missing encoder fails before
any model loads.

## Failure and resume

| Event | Behaviour |
|-------|-----------|
| One turn fails to synthesize | Recorded as `failed` with `duration_ms: 0` and no `wav`, the rest still record, exit is non-zero, `--resume` retries it |
| Every turn fails | No `conversation.wav` is written |
| Interrupted (Ctrl-C) | The turns already recorded are kept and the manifest is written; no partial mix |
| Encoder missing | Fails before any model loads |
| A brain fails while generating | The lines written so far stay in `script.resolved.yaml`, nothing is recorded, exit is non-zero, `--resume` continues from the last line |

`--resume` keeps turns whose audio is already on disk and unchanged; a changed
line, voice, or persona re-records that turn. `--overwrite` re-records
everything. `conversation.wav` is always rebuilt from the turn WAVs, so a
resumed run produces the same recording as a clean one.

`--json` prints the counts a script can branch on:

```bash
obey-voice scene script.yaml --out-dir out/coffee --json | jq '.failed'
```

## Generated dialogue

In `generate` mode you write the brief and the two personas write the lines.
Each persona's own brain — the provider and model its profile names — speaks
for it, in character, with tools, skills and the environment block off. The
recording path is then exactly the verbatim one.

```yaml
schema: obey-voice.scene.v1
id: coffee-counter-improv
title: Coffee counter
mode: generate
cast:
  - id: sam
    persona: samantha
  - id: uncle
    persona: uncle-fu
scene:
  setting: A coffee counter, morning rush.
  beats:                      # what has to happen, in order
    - Uncle dodges why he is late.
    - Sam does not buy it.
  opener:                     # optional written first line, spoken as turn 0
    speaker: sam
    text: "You're late."
  max_turns: 8                # default 24, at most 48
  stop_phrases: ["[END]"]     # a line containing one ends the scene and is kept
```

A committed example lives at
[`testdata/scenes/two-voices.generate.yaml`](../testdata/scenes/two-voices.generate.yaml).
A generate script needs `beats` or an `opener`; `turns` may be left out, or
may hold a written prefix the personas continue from.

How the lines are produced:

- Both brains first read the same briefing: the title, the setting, both
  characters' names, the beats, and an instruction to answer with spoken
  dialogue only.
- The opener, if any, is turn 0 and is never asked for. The other speaker
  hears it as their first input. Without an opener, `cast[0]` speaks first and
  is told only `Speak your first line now.`
- Speakers alternate; every later input is exactly the other character's last
  line. `max_turns` counts every line, the opener included.
- A line containing a stop phrase (case-sensitive, after trimming) ends the
  scene and is kept.

Every generated line is written to `script.resolved.yaml` before the next one
is asked for, and that file is a verbatim script: edit a line and run it again
as the input to re-record without regenerating. It also records a
`generate_fingerprint` of the brief it was written for.

`--resume` on a generate script keeps the lines already written **for this
same brief** — the personas, setting, beats, opener and any written prefix —
replays them into both brains, and asks for the first missing line. Raising
`--max-turns` continues a finished scene rather than restarting it; changing a
beat starts over. `--overwrite` starts over regardless.

`--resolve-only` stops once the lines are written: no TTS asset is checked, no
model is loaded. Use it to read the dialogue before spending the recording, or
to record it later with `--resume`. On a verbatim script it just writes the
scripts.

With `--json`, a run that ends at the resolved script — `--resolve-only`, or a
brain error — reports `turns` and `completed` as the lines written, an empty
`conversation` and `manifest`, and `failed: 1` on an error.

The cgo-free integration binary validates generate scripts and prints their
plan; it generates nothing and records nothing.
