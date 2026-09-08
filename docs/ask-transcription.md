# Ask transcription protocol

Ask captures without starting an agent turn. The client sends these WebSocket controls in order:

1. `{"type":"transcribe_start","id":"<unique recording ID>"}`
2. `audio_input` frames: base64 PCM signed 16-bit little-endian, 16 kHz mono.
3. `{"type":"voice_end"}` when the user presses the hotkey again.

The requesting connection receives `{"type":"transcription_result","id":"<same ID>","text":"recognized question"}`. A failed recognition includes an `error` string. Empty text means no speech was recognized. The client must match the recording ID and discard cancelled or superseded results.

The second press authorizes submission: after receiving a successful nonempty final result, send `text_input` to ask the agent. `interrupt` cancels recognition; it must not precede `voice_end` when submitting, because offline STT needs EOF to finish. Clients can also use the result for an explicit review flow.

Transcription shares the ordinary remote microphone claim, dispatch queue, and recording deadline. It does not invoke the brain, route tools, speak, commit conversation history, or emit a conversational `user_input`/`turn_metrics` event. The terminal result is private to its requesting connection; existing transcript preview events use the session event bus.

Parakeet emits rolling provisional transcripts while recording, including across speech pauses. They may be revised as more audio arrives. Final recognition waits for EOF. Other providers retain their existing partial-transcript behavior.

The Mac client and this engine update must be deployed together. An older engine rejects `transcribe_start`; clients should stop capture and show an update message rather than silently fall back to an agent voice turn.
