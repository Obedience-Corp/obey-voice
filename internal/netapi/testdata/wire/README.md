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
(whisper-small), TTS `kokoro`. All the files come from one serve process.
`audio_chunk` frames are omitted — they are base64 PCM and add megabytes
without adding contract.

| File | Scenario |
|---|---|
| `text_turn.jsonl` | `audio_output{mode:stream}` → `text_input` → one complete text turn |
| `voice_turn.jsonl` | `voice_start` → 3.1 s of **real speech** as `audio_input` at 16 kHz → `voice_end` → a complete voice turn: STT transcript, model reply, two TTS segments. The speech is macOS `say -o … --data-format=LEI16@16000`; a synthetic tone transcribes to nothing and would prove nothing |
| `interrupt_speaking.jsonl` | `interrupt` sent the instant `speaking_started` arrived (`response_delta` frames omitted for size) |
| `interrupt_after_delivery.jsonl` | `interrupt` 4 s **after** `turn_metrics`, while a stream client is still playing the reply. The host thinks the turn is finished; the barge-in is reported anyway |
| `interrupt_idle.jsonl` | `interrupt` on a connection that never heard anything — `audio_reset` and nothing else |
| `ready_banner.json` | the `--banner-json` ready line. `token` and `fingerprint` are `REDACTED` — a live bearer must never be committed, and a test enforces it. Every other field is verbatim |
| `status.json` | `GET /v1/status` off the same serve, `fingerprint` redacted the same way |

## What they are for

`response_delta` and `audio_level` used to reach clients as a bare
`{"type":…}`, and a remote `interrupt` used to answer with `audio_reset` alone.
These captures are the proof that neither is true any more, and the thing an
app-side decoder can be tested against.

`interrupt_after_delivery.jsonl` earns its place separately: with
`audio_output` `stream` the host finishes a reply the moment it hands the chunks
to the socket, so a barge-in ten seconds later looks idle from the server's own
metrics. That is the case that made the feature useless in the deployment it
was built for, and it is captured rather than argued about.
