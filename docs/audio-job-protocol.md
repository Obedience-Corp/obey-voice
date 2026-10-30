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
