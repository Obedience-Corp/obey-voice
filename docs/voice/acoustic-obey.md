# Experimental acoustic recognition of Obey

Parakeet can spell the spoken name as “Heyobe” or “Oh bay.” Enabling
`acoustic_name_enabled` runs a small local keyword model on the same finalized
VAD utterance. Its observation reaches the existing semantic conversation router
alongside the unchanged transcript. No misrecognition aliases are required.

This experiment supports **Parakeet and the shared semantic conversation router**
(TUI/desktop conversations with an addressee-capable brain). It recognizes **Obey**
only. It does not change the legacy prefix gate, other STT providers, typed input,
agent handoffs, microphone activation, speaker authorization, or echo handling.
It runs at utterance end, not as a low-power always-on wake-word listener.

Enable explicitly, then restart the conversation:

```sh
obey-voice config set acoustic_name_enabled true
obey-voice models ensure --stt
```

Use `stt_provider=parakeet`. Setup/startup's existing model installer also includes
the extra model when this option is enabled. The pinned download is about 17 MB;
the default remains disabled. Disable with
`obey-voice config set acoustic_name_enabled false`.

The keyword model is GigaSpeech Zipformer 3.3M, Apache-2.0, with SHA256-verified
installation. The full precision model is used: its quantized encoder missed an
additional synthetic positive in the initial replay. Tokens for OBEY come from
the supplied BPE tokenizer. No Python dependency is needed at runtime.

## Behavior and limits

A keyword hit is evidence of a sound, not proof that the user addressed an agent.
The semantic classifier still decides intent; normal roster lookup still rejects
ambiguous or unavailable identities. When the transcript already contains the
name as a word, the classifier receives its original prompt without an extra
acoustic hint. Evidence exists only for the admission of that voice utterance.
The detector gets a fresh native stream per utterance and does not trim audio.

The native dependency is pinned to sherpa-onnx **1.13.8**. The previous 1.13.4
bundles ONNX Runtime 1.27.0, which silently breaks keyword/Zipformer inference on
SME-capable Apple Silicon: [upstream report](https://github.com/k2-fsa/sherpa-onnx/issues/3791).
The shipped 1.13.8 runtime is 1.28.2. Linux release license checks are updated to
that version; both upstream license hashes remain unchanged.

Initial evidence on an Apple M4, using the nine checked-in **synthetic** clips
([per-clip replay report](../benchmarks/acoustic-obey-initial.json)):

| Condition | Keyword hits / present | False matches / absent |
|---|---:|---:|
| Clean | 5 / 6 | 1 / 3 |
| Added white noise, 10 dB SNR | 3 / 6 | 0 / 3 |

All four clean direct-address positives were detected. “Tell me about Obey” was
missed; “Oh baby” produced a false acoustic hit. This tiny corpus does not measure
ambient false activations per hour or real microphone performance. Detector
processing on the short clean samples was approximately 7–22 ms, after STT.
The native smoke test checks stable positive/negative controls and resource/state
isolation. The separate benchmark reports all challenge cases, including failures;
it does not assert perfect recognition.

A real Silero → Parakeet → detector fixture run produced
`Heyobe, tell me the weather.` with acoustic name `Obey`. Enabling the feature
preserved exactly the same command text; `Okay, tell me the weather.` had no hint.

In the local Qwen3.8 A/B replay, “Oh bay, can you help me?” went from no
addressee to Obey. “Heyobe, tell me the weather.” went from a bare greeting
to a substantive request, preserving the command instead of just acknowledging
the name. These are observed outcomes on these inputs, not general guarantees.

The installed Qwen3.8 model passed the live semantic challenge cases, including
verbs, quotations, mentions, and the “Oh baby” false acoustic hit. Dolphin-Mistral
failed the verb/mention/quotation cases even with the extra hint suppressed. These
results are model-specific: do not promote this experiment to a default or claim
room-level wake-word reliability from these checks.

## Reproduce without microphone or playback

```sh
# Extracted keyword model directory; no user config is loaded by these checks.
OBEY_KWS_MODEL_DIR=/path/to/keyword-model \
  go test -race ./pkg/voiceagent/kws -run NativeReplay -count=1 -v
just bench kws -model-dir /path/to/keyword-model > kws-report.json

# Root containing Silero, Parakeet v2, and the optional KWS directory.
OBEY_KWS_STT_MODELS=/path/to/models \
  go test -race -tags integration ./pkg/voiceagent/stt \
  -run '^TestParakeetAcousticEvidenceNative$' -count=1 -v

# Downloads/checksums/extracts only KWS into a test temp directory, then reuses it.
OBEY_KWS_INSTALL_TEST=1 go test -tags integration ./pkg/voiceagent/config \
  -run '^TestAcousticNameManagedInstall$' -count=1 -v

# Sends only synthetic test sentences to local Ollama; no tools/history changes.
OBEY_KWS_ROUTER_MODEL=qwen3.8:latest go test -tags integration ./pkg/voiceagent/brain \
  -run '^TestAcousticNameLiveRouting$' -count=1 -v
```

Before broad rollout, collect consented human positives across speakers/distances
and long negative room recordings; measure misses, semantic false switches, and
latency on that held-out corpus. The benchmark accepts `-cases FILE`, using the
format in `testdata/kws/cases.json` with WAV paths relative to the manifest.
