# Wire fixtures

Every file here is a **live capture** off `/v1/stream`, one server→client frame
per line, verbatim bytes in arrival order. Nothing here is hand-written; when a
frame's shape changes, re-capture rather than edit.

## How they were captured

A `samantha serve` built from this branch, run under a throwaway `HOME` on a
non-default port, driven by a WebSocket client that recorded every inbound
frame:

```
samantha serve --banner-json --no-voice --no-mdns --bind 127.0.0.1 --port 47262
```

Providers for the capture: brain `ollama` (`llama3.2:latest`), STT `sherpa`
(whisper-small), TTS `kokoro`. `audio_chunk` frames are omitted from the files —
they are base64 PCM and add megabytes without adding contract.

| File | Scenario |
|---|---|
| `text_turn.jsonl` | `audio_output{mode:stream}` → `text_input` → full turn → `interrupt` after the turn had finished (the idle-interrupt case: `audio_reset` alone) |
| `voice_turn.jsonl` | `audio_output{mode:stream}` → `voice_start` → 2 s of `audio_input` at 16 kHz → `voice_end` |
| `interrupt_speaking.jsonl` | `text_input` → `interrupt` sent while TTS was still playing |
| `ready_banner.json` | the `--banner-json` ready line, `token` and `fingerprint` replaced with `REDACTED` (a live token must not be committed; every other field is verbatim) |

## What they are for

`response_delta` and `audio_level` used to reach clients as a bare
`{"type":…}`. These captures are the proof they now carry their payloads, and
the thing an app-side decoder can be tested against.
