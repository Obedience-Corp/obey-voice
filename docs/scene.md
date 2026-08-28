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
```

`--out-dir` is required: a scene always writes several files.

## The script

A scene script is an `obey-voice.scene.v1` YAML document — a cast of exactly two
personas and the turns they speak. Unknown keys are a load error, so a typo
surfaces instead of being ignored.

```yaml
schema: obey-voice.scene.v1
id: coffee-counter
title: Coffee counter
mode: verbatim          # verbatim | generate (generate is not in this build)
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

The full field list, including the `scene:` block generated mode will use, is in
[`docs/schemas/scene.schema.json`](schemas/scene.schema.json).

## What a run writes

```text
out/coffee/
  script.yaml            copy of the input
  script.resolved.yaml   the script as recorded, always verbatim
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
| `--mode generate` | `scene: generated mode is not in this build` |

`--resume` keeps turns whose audio is already on disk and unchanged; a changed
line, voice, or persona re-records that turn. `--overwrite` re-records
everything. `conversation.wav` is always rebuilt from the turn WAVs, so a
resumed run produces the same recording as a clean one.

`--json` prints the counts a script can branch on:

```bash
obey-voice scene script.yaml --out-dir out/coffee --json | jq '.failed'
```

## Generated dialogue

`--mode generate` (write beats, let the personas improvise the lines) and
`--resolve-only` are defined in the schema but not implemented in this build.
Both refuse after validating the script, so a generate script can still be
checked against this binary.
