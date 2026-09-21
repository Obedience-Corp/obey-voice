# obey-transcript/v1

Wire contract for transcribing a finished audio file. A caller writes a request
document, runs `obey-voice transcribe`, and reads one JSON object per line from
stdout. Any local tool in any language can be that caller: the contract is the
process, not a library.

Keepshot is the first client. Host implementation: `cmd/obey-voice/cmd/transcribe.go`
and `pkg/voiceagent/transcription`.

## This is not Ask transcription

Two surfaces in this repository recognize speech without starting a
conversation. They share nothing but the word.

| | `obey-voice transcribe` | [Ask transcription](ask-transcription.md) |
|---|---|---|
| Input | a finished 16 kHz mono WAV on disk | live microphone audio over a WebSocket |
| Transport | a child process, JSONL on stdout | `serve`'s `/v1/stream`, framed messages |
| Output | timed utterances with sample-accurate start and end | one final text for the whole turn |
| Caller | any local tool | the Obey Voice app, on a hotkey |
| Timing | microseconds on the source audio timeline | none |

If you want captions for a recording, you are in the right document.

## Commands

```bash
obey-voice transcribe --request /absolute/job/request.json --jsonl
obey-voice transcribe capabilities --json
```

`--jsonl` is required: there is no other output form. `--request` must be an
absolute path to a readable JSON document.

## The request

```json
{
  "protocol": "obey-transcript/v1",
  "job_id": "take-20260918-001-attempt-1",
  "input": {
    "path": "/absolute/job/audio.wav",
    "format": "wav_pcm_s16le",
    "sample_rate_hz": 16000,
    "channels": 1,
    "timeline_origin_us": 0
  },
  "provider": "configured",
  "language": "en",
  "timing": "utterance",
  "diarization": false,
  "allow_model_download": false
}
```

| Field | Required | Accepted in v1 |
|---|---|---|
| `protocol` | yes | `obey-transcript/v1`, or any `obey-transcript/v1.x` |
| `job_id` | yes | any non-empty string; it appears in every event |
| `input.path` | yes | an absolute path to a readable file |
| `input.format` | yes | `wav_pcm_s16le` |
| `input.sample_rate_hz` | yes | `16000` |
| `input.channels` | yes | `1` |
| `input.timeline_origin_us` | yes | `0`: normalize the file to its playback timeline before submitting |
| `timing` | yes | `utterance` |
| `provider` | no (default `configured`) | `configured`: the backend is whatever `stt_provider` selects |
| `language` | no | a BCP 47 tag, or omitted to use the provider's configuration |
| `diarization` | no (default `false`) | `false` |
| `allow_model_download` | no (default `false`) | `false`: install models with `obey-voice models ensure` |

Unknown fields are ignored, so a request written for a later v1 build still
works. Unknown *values* are not: an enum this build does not implement is an
error, never a silent substitution.

The file's actual WAV header must match what the request declares. The request
is the caller's claim; the header is the fact, and a disagreement is
`input_format_mismatch`.

A request document larger than 1 MiB is `invalid_request`.

## The event stream

Stdout is UTF-8 JSONL and nothing else. Diagnostics, human messages and errors
go to stderr.

Every event carries `protocol`, `job_id`, `seq` and `type`. `seq` starts at 0
and increases by exactly one per line.

The stream is exactly these six types. A client that skips an unknown `type`
would silently drop a segment, so v1 will not add another.

### accepted

Always first. It reports the backend that was resolved, never the preference
string the request sent.

```json
{"protocol":"obey-transcript/v1","job_id":"conformance-eof-speech","seq":0,"type":"accepted","provider":"parakeet","model_id":"tdt-0.6b-v2","model_revision":"157c157b","timing":"utterance"}
```

### segment

One committed utterance. `start_us` and `end_us` are integer microseconds on
the source audio timeline, which starts at the file's first sample. v1 has no
partial or revision events, so `final` is always `true` and text is never
retracted.

```json
{"protocol":"obey-transcript/v1","job_id":"conformance-eof-speech","seq":1,"type":"segment","segment_id":"s1","start_us":800000,"end_us":2400000,"text":"Here is the recording.","timing":"utterance","final":true}
```

Segment starts do not decrease. `start_us < end_us`, and every `end_us` is at
or before the duration the terminal `completed` reports.

### progress

Optional, and throttled. Both numbers are measured, never estimated: there is
no percentage while a model loads.

```json
{"protocol":"obey-transcript/v1","job_id":"conformance-eof-speech","seq":2,"type":"progress","consumed_us":5000000,"duration_us":6300000}
```

### completed

Exactly one of `completed`, `failed`, or `cancelled` ends the stream, and
nothing follows it.

```json
{"protocol":"obey-transcript/v1","job_id":"conformance-eof-speech","seq":4,"type":"completed","duration_us":6300000,"segment_count":2,"outcome":"speech"}
```

`outcome` is `speech` or `no_speech`. Silence is a success: `segment_count` 0,
`outcome` `no_speech`, exit 0. It is not an error and needs no retry.

```json
{"protocol":"obey-transcript/v1","job_id":"conformance-no-speech","seq":1,"type":"completed","duration_us":30000000,"segment_count":0,"outcome":"no_speech"}
```

### failed

```json
{"protocol":"obey-transcript/v1","job_id":"conformance-missing-model","seq":0,"type":"failed","code":"model_missing","message":"model assets are not installed under {{MODELS_DIR}}: silero_vad.onnx; run `obey-voice models ensure`","retryable":false}
```

`code` is stable. Branch on it, not on `message`. `retryable` says whether the
same request could succeed unchanged. A missing model is not retryable until
something installs it; a decode failure may be.

`{{MODELS_DIR}}` (and `{{INPUT}}` in other recorded streams) stand in for
absolute paths, which differ on every machine.

### cancelled

`cancelled` carries only the envelope. Segments already written stay on stdout
and stay partial: the absence of `completed` is what tells you they are not a
transcript.

```json
{"protocol":"obey-transcript/v1","job_id":"conformance-cancelled","seq":2,"type":"cancelled"}
```

## Failure codes

| Code | Exit | Meaning |
|---|---|---|
| `invalid_request` | 2 | The request is missing a field, has a relative path, or names a value that is not a known enum |
| `unsupported_protocol` | 2 | The request names another protocol or another major version |
| `unsupported_capability` | 2 | A named capability this build does not implement: another format, rate, channel count, a nonzero timeline origin, `word` or `none` timing, a named provider, diarization, downloads, or a language the configured model does not transcribe |
| `input_unreadable` | 2 | The input file is absent or cannot be read |
| `input_format_mismatch` | 2 | The file's header disagrees with the request, or the file is not a readable WAV |
| `provider_unavailable` | 3 | The configured `stt_provider` cannot transcribe a file, or its recognizer could not be built |
| `model_missing` | 3 | A required model asset is not installed. Run `obey-voice models ensure` |
| `decode_failed` | 4 | Recognition failed part-way through |
| `internal` | 4 | A failure in this build, including an event line over the size cap |

## Exit codes

| Status | Meaning |
|---|---|
| 0 | `completed`, and only after it |
| 2 | The request is invalid, or asks for a capability this build does not have |
| 3 | The provider or its models are unavailable |
| 4 | Recognition or the runtime failed |
| 130 | Cancelled |

## Client obligations

A client must:

1. **Drain stdout and stderr concurrently.** A child blocked writing to a full
   pipe never exits. Two reader threads, or redirect both to files.
2. **Bound every line at 1 MiB, including the newline, before parsing it.**
   The producer enforces the same cap and replaces an oversized event with a
   `failed` carrying code `internal`.
3. **Reject a stream that breaks the envelope rules:** a `protocol` that is not
   `obey-transcript/v1`, a `job_id` that is not the one submitted, a `seq` that
   is not the next one, a segment with `end_us <= start_us`, a segment that
   starts before the previous one, or any event after the terminal event.
4. **Require both a terminal `completed` event and exit status 0** before
   treating a transcript as finished. EOF without a terminal event is a
   failure. `completed` followed by a nonzero exit is a failure.
5. **Never build the command through a shell.** The request path is an
   argument, not a string to interpolate.
6. **Own its job IDs.** This command keeps no state between runs: the same ID
   submitted twice produces two independent, identical streams. Deduplication
   belongs to whatever assigns the IDs.

`docs/transcribe-protocol/client-example.sh` implements all six in POSIX shell
and `jq`, in about a hundred lines, importing nothing from this repository.

## Cancellation

SIGINT or SIGTERM cancels through the context: the stream ends in `cancelled`
and the process exits 130. The budget is 2 s from signal to exit. A second
signal, or three seconds of wedged shutdown after the first, force-exits 130
regardless.

A cancelled job is not retried automatically. That is the caller's decision.

## Capabilities

`obey-voice transcribe capabilities --json` is read-only and offline: it
resolves the configured provider, reads the asset tables and the models
directory, and constructs no recognizer, opens no microphone and downloads
nothing. Run it before submitting a job, and on a setup screen.

It reports the accepted protocol versions and formats, the timing
granularities, one row per implemented provider with its languages and whether
it can transcribe a file at all, the selected provider's models and their
install state, readiness with the first blocker named, whether anything must be
installed, and the setup diagnostics that apply to transcription.

`languages` lists the codes the resolved model is known to transcribe. An empty
list with `auto_detect: true` means the model accepts more than this build
enumerates, and a request's `language` is passed through to it.

Without `--json` the same facts print as human text. The machine contract is
the JSON document.

## Timing

`utterance` is the only granularity v1 implements. Times come from the speech
detector's spans on the source timeline, including any leading and trailing
speech the detector retained, not from when recognition happened to finish.
Starts round down: `start_us = floor(start_sample * 1000000 / 16000)`. Ends and
the file duration round up, so a span that reaches the last sample equals the
`duration_us` that `completed` reports.

`word` is named by the schema and advertised by nobody: no shipped provider
produces word alignments, and dividing an utterance by its word count would be
an invention. A request asking for it is rejected with
`unsupported_capability`. `none` is likewise rejected: v1 requires `utterance`.

Timings are estimates from a speech detector and a recognizer. Treat them as
accurate to a few hundred milliseconds, not to the sample.

## Versioning

The major version is the compatibility boundary. Within `v1`:

- fields are added, never removed or renamed;
- event types are not added, because a client that skips an unknown type would
  silently drop a segment;
- failure codes may be added, and a client that does not recognize one treats
  it as a runtime failure.

A breaking change is `obey-transcript/v2`, and this build rejects it with
`unsupported_protocol`.

## Recorded streams

`docs/transcribe-protocol/fixtures/` holds one recorded stream per scenario,
generated from the conformance suite and carrying the commit they came from.
Implementations test against those files rather than against a running command.

| File | Scenario | Terminal event | Exit |
|---|---|---|---|
| `eof_speech.jsonl` | two utterances, the second ending at the file's last sample | `completed`, `speech` | 0 |
| `no_speech.jsonl` | 30 s of silence | `completed`, `no_speech` | 0 |
| `cancelled.jsonl` | interrupted after one segment | `cancelled` | 130 |
| `bad_version.jsonl` | a request naming a later major version | `failed`, `unsupported_protocol` | 2 |
| `unsupported_timing.jsonl` | `timing: word` | `failed`, `unsupported_capability` | 2 |
| `unsupported_language.jsonl` | a language the configured model does not transcribe | `failed`, `unsupported_capability` | 2 |
| `wrong_rate.jsonl` | a 48 kHz file described as 16 kHz | `failed`, `input_format_mismatch` | 2 |
| `missing_model.jsonl` | the model assets are not installed | `failed`, `model_missing` | 3 |

A protocol change means regenerating them
(`go test ./cmd/obey-voice/cmd -run TestTranscribeConformanceFixtures -update-transcribe-golden`),
copying them into `docs/transcribe-protocol/fixtures/`, updating the source
commit in its README, and telling the consumers. Keepshot carries its own
copies under `src/transcript/testdata/` and refreshes them by hand.
