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
script; it does not claim to reproduce the source recording. V1 cannot substitute narration for conversion or dubbing. Reviewed dubbing has
its own explicit v3 request below; recording-preserving conversion and measured
alignment remain unavailable.

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

## Audio-input text artifacts (v2 extension)

The same finite command accepts `obey-audio-job/v2` for `transcription` and
`understanding`. V1 narration requests/manifests and transcript v1/v2 clients
remain unchanged. V2 is explicit because its output is JSON rather than WAV;
a v1-only consumer must reject it. This first v2 route uses the managed pack,
not an assistant or external server. It inherits the v1 source/config identity,
private storage, cancellation, event ordering and atomic publication rules.

A v2 request keeps the v1 source/input/runtime/limits envelope, uses English,
an empty `script`, no target language and `duration_policy: "preserve_picture"`.
`understanding` requires an explicit `instruction` of at most 1024 UTF-8 bytes;
`transcription` requires an empty instruction and uses the fixed versioned ASR
prompt. Recording content is data. Neither task advertises or executes tools.

Example task fields within the existing envelope:

```json
{"protocol":"obey-audio-job/v2","task":"understanding",
 "instruction":"Describe the sounds and spoken topics in this recording.",
 "script":"","language":"en","duration_policy":"preserve_picture"}
```

Each job covers the full input with 30-second windows and two seconds of
overlap (`fixed-30s-overlap-2s/v1`). The final window ends at the exact input
sample count; windows carry absolute source sample offsets and the SHA-256 of
the actual WAV sent to the model. No inference history or persona context
compaction is used. Overlap is retained as reviewable context, not silently
deduplicated into invented word timings. Output limits and the whole-job
deadline bound work; failure in any window publishes no artifact.

The runtime receives WAV data directly through `/v1/chat/completions` and
returns bounded text. Token-count preflight must show that the complete audio
window and reserved output fit the owned context. A length-limited, tool-call,
malformed, empty or incomplete response is a failure, not a successful finding.
The request and manifest bind the instruction/prompt hash, pinned chunk policy,
model/projector/runtime digests and text sampling settings.

The v2 manifest records `transport: "audio_to_text"`. Its `output` has
`kind: "json"`, `path: "findings.json"`, SHA-256 and byte count; it has no audio
sample geometry. `findings.json` uses `obey-audio-findings/v1`, with the task and
ordered windows containing source start/end samples, input-window SHA-256 and
model text. Window ranges mean audio coverage, not measured claim locations or
word alignment. Findings are unqualified drafts for explicit review; ASR gaps,
no-speech prose and empty text never establish silence or authorize cuts.

Compatibility: a v1 narration still produces its unchanged WAV/manifest pair;
a v2 text task produces findings/manifest and cannot enter the audio-take import
path. V2 cannot create a spoken take. Reviewed dubbing uses the explicit v3 request
below; recording-preserving conversion and measured alignment remain unavailable. The editor must validate this new major
version explicitly before displaying or saving findings.

`obey-voice transcribe audio-capabilities --audio-config /absolute/config.json
--json` verifies the selected managed assets, starts the owned runtime, reads
its modalities and emits `obey-audio-capabilities/v1`. The document pins the
runtime/model/projector digests, input format, voices/languages, task protocols,
script/instruction limits and chunk policy. Every task has separate supported,
installed and qualified fields plus a reason. This metadata check performs no
inference and qualifies no task; each actual text job also checks the token-count
route and complete generation response. No download or assistant initialization
occurs, and the owned server is reaped on exit or cancellation.
`installed` describes the verified selected pack, including when a particular
task remains unsupported; it never implies that an aligner or conversion
implementation is available.


## Translation drafts (additive v2 task)

`task: "translation"` accepts the same source-audio envelope and JSON findings
output as the other v2 tasks. The caller explicitly selects `language` and
`target_language`. The first adapter pair is `en` (English) and `es` (Spanish),
in either direction; identical, missing or other languages fail before runtime
startup. `script` and `instruction` must be empty. Existing English transcription,
understanding and v1 narration requests retain their behavior. Consumers must
validate the task, not assume every v2 result is an original-language transcript.

```json
{"protocol":"obey-audio-job/v2","task":"translation","script":"",
 "language":"en","target_language":"es","duration_policy":"preserve_picture"}
```

The fixed system prompt follows [the model card's translation
instructions](https://huggingface.co/LiquidAI/LFM3-Audio-2B#translation):
`Translate the following speech from English to Spanish in text form.`
Only allowlisted language names enter that prompt; recording content is the
separate audio message, with no tools. The actual prompt hash and both language
codes are retained in the manifest. The findings also retain both languages so
an exported draft remains unambiguous outside its job directory.

All input windows, overlap, token preflight, terminal-response checks, resource
limits and atomic publication rules apply unchanged. Overlapping drafts must be
reviewed before becoming a spoken script: concatenating windows can duplicate
words, and window endpoints are not subtitle or word timings. This task produces
no audio, edits or cuts. A later explicit reviewed-script generation request is
required for dubbing; requesting translation never silently synthesizes speech.
Capability metadata reports source and target language choices separately and
keeps `qualified: false`. Transport tests and a completed model reply do not
qualify meaning preservation, pronunciation or timing.

## Reviewed dubbing previews (v3)

`obey-audio-job/v3` adds `task: "dubbing"` for the managed pack. It is a
separate explicit request after translation review. Source and target languages
must be distinct `en`/`es`; the caller supplies the approved target-language
`script` (1–1024 UTF-8 bytes), empty instruction and the existing
`preserve_picture` policy. V1 narration and v2 findings remain unchanged.

The required `translation_review` object contains `reviewed: true`, the exact
v2 translation `manifest_json` and `findings_json` strings. These strings contain
the original JSON documents, preserving their bytes and trailing whitespace. The finite
worker checks the findings bytes against their manifest hash, language pair,
source/range/revision, original WAV identity, complete window plan, prompt and
runtime/model/projector digests. The review capsule is bounded to 256 KiB. A
missing, corrupt, stale or differently sourced translation fails before model
startup. The explicit review assertion means the caller has shown the text and
received approval; the model cannot provide this approval itself.

```json
{"protocol":"obey-audio-job/v3","task":"dubbing",
 "script":"El texto aprobado.","language":"en","target_language":"es",
 "duration_policy":"preserve_picture",
 "translation_review":{"reviewed":true,"manifest_json":"<original manifest JSON>","findings_json":"<original findings JSON>"}}
```

The placeholder strings above stand for complete validated v2 artifacts,
not valid requests. Preserve the original JSON bytes inside the review capsule;
reformatting findings changes their hash. The new speech request sends only the
approved script to `/tts`, with the explicit target `lang` and selected voice.
It never synthesizes raw overlapping translation windows automatically.

The v3 WAV manifest records `transport: "reviewed_translation_then_speech"`,
source and target languages, `approved_script`, its hash, the complete bounded
`translation_review` capsule and its original manifest hash. Keeping this proof
in the manifest makes the generated take independent of translation caches.
Exact output sample count exposes duration mismatch. Clients must review it and
choose a supported timeline policy before Apply; this worker performs no
stretching, trimming, alignment, source replacement or picture retiming.
Dubbing is an unqualified preview, not recording-preserving voice conversion.


## v4 recorded-waveform conversion (WI-43b072)

`obey-audio-job/v4`, task `voice_conversion`, is a distinct waveform operation.
It uses the shared native OpenVoice component extracted in Keepshot #174. It
never invokes chat, ASR or TTS, and does not interpret a script as a reference.
Transport support and installed assets remain separate from quality qualification;
this first adapter is experimental and unqualified for intelligibility/delivery.

The existing isolated `obey-voice.audio-config/v1` gains an optional `waveform`
pack. It is mutually exclusive with `managed` and requires runtime provider
`openvoice`, model `openvoice-v2`, empty endpoint and voice `reference`. The pack
contains hash-pinned absolute `executable`, `encoder`, `converter` and
`runtime_library` files and version `openvoice-v2-onnxruntime-1.30.0`. The three
native assets share one pack directory under their fixed names; the executable
is an independently pinned `keepshot-voice-worker`. No assistant globals or
server are read or changed, and no network/download route exists in this adapter.

The request preserves existing job/source/project/range identity and resource
limits. `input` is a nonempty PCM16 mono WAV at 22,050 Hz, at most one hour, with
explicit SHA, sample count and source origin. Only microphone/speech channel
roles are admitted; mixed recordings need explicit separation before conversion.
`reference` is another hash-addressed PCM16 mono 22,050 Hz WAV, 1–15 seconds, role
`reference`, origin 0. Reference identity is retained independently of the source.
`script`, `instruction`, `target_language` and `translation_review` must be empty;
`language` is `en` for the initial adapter experiment. `duration_policy` remains
`preserve_picture`. Output budget must cover exactly 44 + 2 × input_samples bytes.

Execution verifies inputs and every installed asset before starting the native
helper. It snapshots decoded PCM into private f32le files, invokes the helper
with explicit pack/source/reference/output paths, bounds receipt/error output,
and owns/reaps its process group. Cancellation, timeout, changed input/reference,
wrong helper receipt, malformed/truncated WAV, sample-count mismatch or a failed
worker exit leave no published artifact. Existing ordered events and atomic
publication apply; progress stage is `waveform_conversion`.

The v4 manifest records source/input/reference identities, actual transport
`waveform_conversion`, `reference` voice, selected language, exact source range,
worker/model/encoder/runtime hashes, native runtime version, zero-temperature
conversion, output 22,050 Hz mono PCM16 geometry/hash and chunk policy
`10s-core-1s-context-linear-crossfade/v1`. Source windows are the nonoverlapping
10-second cores; each inference adds at most one second of context on each side.
No word alignment is implied. Output sample count must equal input sample count;
only known internal model-hop padding is removed, never arbitrary retiming.

The native helper contract is `keepshot.recorded-voice-worker/v1`:
`--version` returns one bounded JSON capability document with exact pins;
`convert PACK SOURCE.f32 REFERENCE.f32 NEW.wav TIMEOUT_MS` converts explicit
22,050Hz mono f32le files and returns one JSON receipt containing source/reference/
output SHA-256, sample count/rate, transport and chunk policy. The outer facade
owns source/project provenance, WAV decoding, events and publication. The helper
uses the shared converter, no recorder-private modules. V1/V2/V3 retain their
existing input rates, tasks and validation; no request silently changes task.

Examples (paths/hashes are placeholders, not a runnable installed pack):

```json
{"schema":"obey-voice.audio-config/v1","storage_root":"/private/editor/jobs","runtime":{"provider":"openvoice","endpoint":"","model":"openvoice-v2","voice":"reference"},"waveform":{"version":"openvoice-v2-onnxruntime-1.30.0","executable":{"path":"/private/editor/worker/keepshot-voice-worker","sha256":"WORKER_SHA256"},"encoder":{"path":"/private/editor/pack/tone_extract.onnx","sha256":"ENCODER_SHA256"},"converter":{"path":"/private/editor/pack/tone_color.onnx","sha256":"CONVERTER_SHA256"},"runtime_library":{"path":"/private/editor/pack/libonnxruntime.1.30.0.dylib","sha256":"LIBRARY_SHA256"}}}
```

```json
{"protocol":"obey-audio-job/v4","job_id":"conversion-preview","task":"voice_conversion","source":{"media_sha256":"SOURCE_MEDIA_SHA256","clip_id":"clip1","range_id":"2-12","project_revision":"EDIT_SHA256"},"input":{"path":"/private/editor/jobs/input.wav","sha256":"INPUT_SHA256","sample_count":220500,"sample_rate_hz":22050,"channels":1,"channel_role":"microphone","source_origin_samples":44100},"reference":{"path":"/private/editor/jobs/reference.wav","sha256":"REFERENCE_SHA256","sample_count":176400,"sample_rate_hz":22050,"channels":1,"channel_role":"reference","source_origin_samples":0},"runtime":{"provider":"openvoice","endpoint":"","model":"openvoice-v2","voice":"reference"},"language":"en","duration_policy":"preserve_picture","output_dir":"/private/editor/jobs/take","limits":{"timeout_seconds":300,"output_bytes":441044}}
```

The [native transport proof](audio-jobs/evidence/waveform/README.md) records
network-denied success and cancellation after a converted chunk. Reproduce with
`just test waveform-proof WORKER CONFIG REQUEST NEW_OUTPUT DENY_NETWORK_PROFILE`.
