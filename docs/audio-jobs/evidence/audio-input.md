# Finite native audio-input evidence

WI-43b072, Apple Silicon macOS, 2026-10-30 local date. See the
[structured receipt](audio-input.json) and [v2 contract](../../audio-job-protocol.md).
These are terminal worker and transport checks; editor findings UI, independent
transcription accuracy, word alignment and task quality remain separate work.

The owned b11960 runtime received actual WAV data through its documented
[chat-completion audio input](https://github.com/ggml-org/llama.cpp/blob/8642541b2/tools/server/README.md#post-v1chatcompletions-openai-compatible-chat-completions-api).
The fixed transcription prompt follows the
[model card's ASR example](https://huggingface.co/LiquidAI/LFM3-Audio-2B#usage-examples).
Token counting returned 395 tokens for a 30-second ASR window, equal to the
actual request usage. The separate audio question returned 431/431. Production
requests now require matching preflight/actual counts, reserve 1024 output tokens
within the owned 8192-token context and reject incomplete/tool-call replies.

## Full coverage and lifecycle

The licensed AMI fixture described in [the existing evidence](README.md) was
concatenated three times into a new 90-second PCM16 WAV. Its own hash identifies
that synthetic repetition fixture; it is not presented as an unedited 90-second
meeting excerpt. The v2 transcription job sent four complete windows:

| Source range | Input samples | Actual prompt/completion tokens |
|---|---:|---:|
| 0–30s | 480000 | 395 / 78 |
| 28–58s | 480000 | 395 / 77 |
| 56–86s | 480000 | 395 / 63 |
| 84–90s | 96000 | 95 / 12 |

The harness independently reconstructed every window's WAV from source PCM and
matched its recorded hash. Every requested sample is covered. Overlap is kept
explicit for review; this does not establish deduplicated words, natural seams
or measured word times. The complete findings JSON/manifest published in 4.57s.
Output SHA-256: `9e4aa6e6df7ea752206c00943769662323c20f0dae15f42979642fc09ed87488`.

SIGTERM after acceptance stopped in 0.23s with no artifact or incomplete files.
Forced worker-group termination stopped in 0.29s with no owned runtime left;
the caller removed incomplete scratch after reaping, as the editor client does.
Source bytes remained unchanged. The loopback-only sandbox profile denied
external access; an independent connection to 1.1.1.1:443 returned EPERM.
No microphone, speaker, assistant or model download was used.

## Real source range and task choice

A separate understanding job used the actual 2–12s PCM slice of the cropped
30-second source. Its source-media hash was independently matched against the
video, input count was 160000 samples, and its retained source interval was
32000–192000 samples. Asked which product the speakers were discussing
designing, the model returned “A remote control”. The JSON hash, full terminal
event sequence and source interval were checked under the network-denial policy.

An earlier caller-metadata trial incorrectly used the original meeting's 130s
origin with the cropped source identity. That raw trial is retained and excluded
from range evidence; the independent 2–12s run above corrected the fixture.
The finite worker can validate WAV/hash/geometry but the editor remains
responsible for mapping ranges to its actual source and rejecting stale results.

The real `audio-capabilities` command verified the pack and reported narration,
transcription and understanding adapter support, with every task unqualified.
Alignment, conversion and dubbing remained unsupported. Metadata inspection
performed no inference and exited with no owned server/scratch remaining.

## Reproduction and checks

Supply an explicit config and a v2 request matching the selected fixture. The
existing recipe now handles either v1 WAV output or v2 findings JSON:

```sh
just test managed-audio-proof /absolute/worker /absolute/config.json /absolute/request.json /new/proof/output docs/audio-jobs/evidence/loopback-only.sb
```

`just test audio-jobs` passes under the race detector, including the legacy
narration path and new bounded-window/cancellation/source-mutation checks.
HTTP fixtures reject missing modalities, context overflow, token-count mismatch,
wrong model/runtime, length stops, tool calls, empty/oversized/truncated replies
and stalled requests. The complete 61-second fixture preserves exact PCM and
nonzero source offsets. `just build`, the existing `TestTranscribe` command
regressions under the race detector, and changed-package golangci-lint also pass
(zero lint issues). This evidence does not qualify
filler preservation, language/voice quality, conversion, captions or the editor
consumer's still-unimplemented v2 review/persistence flow.
