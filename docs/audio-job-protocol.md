# Finite generated audio jobs

`obey-audio-job/v1` is an additive artifact protocol; transcript v1/v2 stay unchanged.
The entry point is `obey-voice transcribe audio-job --audio-config /absolute/audio-config.json --request /absolute/request.json --jsonl`.
It requires an explicit isolated request; it never loads assistant settings, starts
capture, uses a persona, tools, a playback device, or downloads models.

Requests contain `protocol`, `job_id`, `task`, `source` (media SHA-256,
clip/range IDs, project revision), `input` (absolute immutable WAV path, SHA-256,
sample count/rate/channels, channel role, source origin samples), `runtime`
(provider, loopback endpoint, model, voice), `script`, `language`, `target_language`,
`duration_policy`, `output_dir`, and `limits` (timeout seconds, output bytes).
Every identity is echoed in the manifest. Narration requires a caller-approved
script; it does not claim to reproduce the source recording. Conversion, dubbing
and alignment require independent qualified capabilities and cannot fall back to
narration or transcript-then-speech.

Each JSONL event has protocol, job_id, consecutive seq starting at zero and type.
The order is accepted, zero or more progress events, artifact, completed. Failed
or cancelled is terminal and may occur before acceptance. Progress may identify
an indeterminate stage; invented percentages are forbidden. An artifact event is
provisional until completed **and worker exit zero**. Clients also verify file
hashes, WAV geometry, manifest identities and current project revision before
accepting it. Partial output is removed on failure/cancellation. Tab changes do
not cancel; source replacement/project retirement do.

Output is a private job directory containing audio.wav and manifest.json,
published atomically only after validation and fsync. A manifest records source
identity/input metadata, script SHA-256, actual transport, runtime/model digest,
output SHA-256/sample count/format and full-coverage source windows. Timing windows
are provenance, never inferred word alignment. Long inputs are validated by
streaming; no persona 30-second cap or silent truncation applies. Per-task runtime
limits are explicit. Generated audio must be copied into project-owned assets
before saving a project reference; inference cache is not durable project storage.

Capability state separates supported, installed and qualified, with reasons.
Ollama's proven speech endpoint can support explicit-script narration via
`text_then_speech`. It does not establish `direct_audio_to_audio`, voice conversion,
dubbing or word alignment. Unsupported tasks fail before inference.

The caller-owned isolated audio configuration is authoritative for endpoint,
model and storage root. Requests must match it. No global settings, environment
credentials or request-selected remote endpoint may override it. Artifact bytes
are bounded WAV files, never base64 JSONL. Output paths must remain beneath the
configured owned root; symlinks and traversal are rejected before staging and
publication. The transport enum is `text_then_speech`, `chat_then_speech`,
`transcript_then_speech`, or `direct_audio_to_audio`; the implementation records
only the route actually used. Capability bits never imply task qualification.

The audio config schema is `obey-voice.audio-config/v1`, with `runtime`
(provider, endpoint, model, voice) and an absolute private `storage_root`.
The request runtime must exactly equal this config. An existing output job
fails without overwrite, even if the request ID matches; explicit retries use
new IDs and directories. Accepted project assets are never job output paths.
Manifest protocol uses the same version: additive fields are allowed within v1;
removing or changing field meaning requires a new major protocol. Consumers
must reject unknown major versions and transports. `preserve_picture` never
stretches audio: any duration mismatch is reported through exact sample counts
and requires explicit timeline review. Apply must reject an unsupported mismatch
rather than silently retime picture.

## Editor-owned llama.cpp runtime

The additive `managed` config selects a finite editor-owned runtime. Existing
Ollama configs keep their behavior. With `runtime.provider: "llama.cpp"`, endpoint
must be empty, model must be `lfm3-audio-2b`, and voice must be `female` or `male`.
The worker chooses a private loopback port and ephemeral authentication token;
these are not project identity and are never used to find an existing server.

```json
{
  "schema": "obey-voice.audio-config/v1",
  "runtime": {"provider":"llama.cpp","endpoint":"","model":"lfm3-audio-2b","voice":"female"},
  "storage_root":"/absolute/private/jobs",
  "managed": {
    "executable":{"path":"/absolute/pack/llama-server","sha256":"<64 lowercase hex>"},
    "model":{"path":"/absolute/pack/model.gguf","sha256":"<64 lowercase hex>"},
    "projector":{"path":"/absolute/pack/projector.gguf","sha256":"<64 lowercase hex>"},
    "version":"b11960-8642541b2"
  }
}
```

The editor verifies the complete installed pack inventory, including libraries,
before invoking the bundled worker. The worker independently hashes these three
regular files, checks the pinned runtime version, starts offline with an isolated
environment, and verifies the loaded alias and audio-output capability. No model
is downloaded. Each job owns one server; cancellation and worker exit reap it.
It inherits the worker's process group, so the editor's final group kill also
reaches the server. An existing Ollama service is neither reconfigured nor killed.

Narration uses the pinned runtime's `/tts` endpoint with explicit English text,
voice, float32 PCM, seed 42, repetition penalty 1.05 and a generation ceiling of
8192 acoustic frames. Managed narration currently accepts at most 1024 UTF-8
bytes; larger scripts fail before starting the runtime. The byte count is a task
limit, not a claim that the resulting speech preserves every word. Output sample
bounds and timeout still apply, and generated speech requires review. This path
never interprets narration as recording-preserving voice conversion.

Managed manifests add `projector_digest`, `runtime_digest` and `sampling` (seed,
max_audio_frames, repeat_penalty). Runtime/model identity stays equal to the
request. Support, successful installation and independent quality qualification
remain separate; this extension does not qualify voice conversion, dubbing,
alignment, or narration quality. The owning runtime protocol is documented in
[the pinned server README](https://github.com/ggml-org/llama.cpp/blob/8642541b2/tools/server/README.md#post-tts-generate-speech-audio-from-text).
