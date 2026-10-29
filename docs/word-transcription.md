# Verbatim word estimates: obey-transcript/v2

Design **WI-37d92f** adds explicit finite-file word analysis for Keepshot Editor.
The existing [v1 utterance API](transcribe-protocol.md) and conversational speech
correction are preserved. No microphone, agent conversation or hosted fallback
is involved. A local `obey-voice` binary is a prerequisite; the editor can manage
speech models through it without reimplementing a recognizer.

## Isolated setup

Create an absolute-path JSON file owned by the calling application:

```json
{
  "schema": "obey-voice.speech-config/v1",
  "provider": "parakeet",
  "model": "tdt-0.6b-v2",
  "models_dir": "/absolute/editor/storage/speech/models",
  "language": "en"
}
```

```sh
obey-voice transcribe --speech-config /absolute/speech.json capabilities --json
obey-voice transcribe --speech-config /absolute/speech.json setup
```

Only `setup` downloads. It requires `--speech-config`, installs the pinned STT
and VAD assets through the existing asset manager, and streams its JSON progress
and terminal summary. No TTS assets are requested. The explicit config bypasses
global settings, environment overrides, assistant/persona initialization,
corrections and acoustic-name rewriting. It neither reads nor changes a user's
assistant configuration. Model storage remains at the caller's absolute path.

Parakeet TDT 0.6B v2 is an NVIDIA model under
[CC BY 4.0](https://huggingface.co/nvidia/parakeet-tdt-0.6b-v2), converted to the
pinned int8 sherpa-onnx artifact already used by obey-voice. The current runtime
is sherpa-onnx 1.13.8. Existing asset manifests supply URLs, checksums and required
files; this command does not invent a second model registry or updater. Setup
repairs missing assets for this binary's pinned revision. Runtime updates remain
part of updating the obey-voice executable.

## Negotiation and request

`capabilities --json` keeps the v1 schema and adds `word_analysis` with protocol,
provider, supported, text_policy, timing_method, timing_validity,
`batch_qualified: false`, and a reason. Its legacy timing/provider tables retain
their v1 utterance meaning. Check both support and `readiness.ready`; support is
not a claim that the assets are installed. Other configured providers cannot
fall back to Parakeet or another runtime for a word request.

```json
{
  "protocol": "obey-transcript/v2",
  "job_id": "editor-job-1",
  "input": {
    "path": "/absolute/audio.wav",
    "format": "wav_pcm_s16le",
    "sample_rate_hz": 16000,
    "channels": 1,
    "timeline_origin_us": 0
  },
  "provider": "configured",
  "language": "en",
  "timing": "word",
  "text_policy": "verbatim",
  "diarization": false,
  "allow_model_download": false
}
```

Run `obey-voice transcribe --speech-config /absolute/speech.json --request
/absolute/request.json --jsonl`. The exact v2 protocol name, word timing and
verbatim policy are required. v1 still rejects word timing. Download and
diarization requests remain unsupported. The finite PCM input origin is zero;
clients are responsible for preserving the source media's audio offset while
decoding to PCM.

## Events, word construction and failures

The envelope and event names are the same as v1, with `protocol` set to v2.
`accepted` additionally records `text_policy: verbatim`,
`timing_method: parakeet-tdt-token-duration`, `timing_validity: estimated`,
language and runtime, alongside provider/model/archive revision.

Each final segment carries `words`, each with a unique `word_id`, raw `text`,
`start_us` and `end_us`. Both ends are measured estimates relative to source PCM,
not offsets from the segment or wall-clock completion. Whitespace-joined words
must reproduce segment text. Token spelling, punctuation, fillers and repeated
words are retained when the recognizer emits them. “Verbatim” means no downstream
correction stage; the ASR model itself can still omit, normalize or hallucinate.

Space-prefixed native tokens form words. Punctuation remains in word text but
does not extend the acoustic edge. Starts and ends come from native TDT token
starts/durations, rounded to the 16 kHz sample clock and then integer microseconds.
Only sub-sample floating-point error is canonicalized. Missing/zero durations,
real overlaps, non-finite/backwards times and out-of-PCM bounds fail the job;
ends are never inferred from the next word or utterance length. Native token log
probabilities are not exposed as calibrated word confidence.

Word mode feeds every sample in nonoverlapping windows up to 20 seconds, choosing
an 80 ms low-energy valley in the last two seconds as a batching boundary. It
does not discard quiet audio with the legacy utterance VAD. Exact-zero windows
can return no speech. A forced split can still hurt recognition; these are
batching boundaries, never proposed silence cuts. v1 retains its prior VAD and
speech-floor behavior.

A successful consumer requires `completed` **and** exit zero, with no data after
the terminal event. Reject missing acceptance/provenance, wrong job/sequence,
invalid/duplicate/out-of-order words, raw-text mismatch, a count/duration mismatch
and all partial results. `failed`, `cancelled` or a nonzero exit leave the previous
transcript intact. Existing exit codes still apply, including 3 for missing
models, 2 for unsupported capability, and 4 for decode failure. Cancellation is
checked around each bounded native decode; an embedding application may kill
and reap the finite child to cancel an in-progress native call immediately.

`protocol.NewWordEncoder` and `NewWordDecoder` explicitly select v2. The original
constructors continue to encode/decode v1. The runner requires `stt.WordDecoder`
and validates its intervals before emitting them.

## Quality and delivery boundary

[Real-speech measurements and reproduction steps](word-timing/README.md) show
that this model does not qualify unattended cleanup. Keep batch disabled and
review each proposed cut. Meaningful “like” is not a spelling filler. Recognition
failure is not silence. Speaker overlap is not diarized. No voice changing,
avatar replacement or automatic acoustic alignment is introduced by this API.
