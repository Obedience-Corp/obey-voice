# Recorded-waveform transport proof

WI-43b072. See [protocol v4](../../../audio-job-protocol.md#v4-recorded-waveform-conversion-wi-43b072)
and the shared Keepshot `keepshot-voice-worker` adapter. This is experimental
OpenVoice conversion, with no ASR/chat/TTS substitution and no quality pass.

`native.json` records a real file-only run on Apple Silicon macOS with all
networking denied for both Obey Voice and its native child. Source: licensed AMI
ES2002a headset0, 130–155.003 seconds; reference: ES2003a headset0, 232–240 seconds.
The existing AMI corpus licensing/source records are in the sibling qualification
evidence. No corpus audio or model weights are committed here.

The successful job took 9.48 seconds and preserved exactly 551,317 mono PCM16
samples at 22,050 Hz, including the final partial chunk. The probe independently
verifies source/reference identities, all pack hashes, exact output geometry and
hash, complete source-window coverage, and ordered events. Only `audio.wav` and
`manifest.json` are published. The original request/manifest hashes are retained;
private absolute paths are omitted from this checked-in summary.

A second job was cancelled only after observing a live owned native worker and
441,044 bytes of converted partial WAV (the first ten-second core). SIGINT caused
a cancelled terminal event and process exit in 0.0122 seconds; the native child
was reaped, no take published, and no stage, lock or version scratch remained.
This verifies interruption after actual native conversion began, not only a
cancelled preflight.

The initial real proof found native caches/debug logs in the published folder.
The adapter now uses and removes a separate runtime HOME/cwd, and the subprocess
fixture reproduces those side effects. The initial sandboxed Python harness could
not execute `ps`; process observation now runs outside the sandbox while every
worker launch remains inside it. Failed runs are retained in the private proof
folder and are not counted as passes.

Reproduce with explicit installed assets and a fresh output directory:

```sh
just test waveform-proof bin/obey-voice CONFIG.json REQUEST.json NEW_OUTPUT DENY_NETWORK.sb
```

Use a multi-window v4 request and the sandbox profile `(version 1) (allow default)
(deny network*)`. The harness reads no assistant config, captures no microphone,
and plays no audio. Its inputs are authorized source/reference files. Unit tests
also reject task substitution, mixed speech, invalid references, stale reference
bytes, bad output geometry/receipts, trailing JSON, output floods and stalled
workers. `just test audio-jobs` passes under race; scoped lint and `just build`
pass. After the runtime-scratch repair, the conversion race suite passes again.

Intelligibility, reference similarity, delivery and seam quality remain
unqualified. Editor model installation, reference selection, preview/Apply,
project relocation and mixed-track export require separate editor acceptance.
