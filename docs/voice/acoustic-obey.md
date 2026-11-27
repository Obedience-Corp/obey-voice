# Experimental acoustic recognition of Obey

Parakeet can spell the spoken name as “Heyobe” or “Oh bay.” Enabling
`acoustic_name_enabled` runs a small local keyword model on the same finalized
VAD utterance. Its observation reaches the existing semantic conversation router
alongside the unchanged transcript. No misrecognition aliases are required.

This experiment supports **Parakeet and the shared semantic conversation router**
(TUI/desktop conversations with an addressee-capable brain). In the terminal and
desktop apps it recognizes **Obey** only; a Go program that embeds the voice agent
gets its own names, see [Other names](#other-names-for-embedding-programs). It
does not change the terminal app's prefix gate, other STT providers, typed input,
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

## Other names, for embedding programs

A program built on `pkg/voiceagent` that sets `acoustic_name_enabled` next to
`name_summon_enabled` gets the detector listening for its own `agent_name` and
`name_summon_aliases`, and its name summon gate uses what the detector hears.
The terminal app, the desktop app and `obey-voice serve` are unchanged: they
still listen for "Obey" and their gate still reads the transcript alone.

What it is for: a recogniser that misspells an invented name. "Juno, what's the
weather" transcribed as "Juneau, what's the weather" is vetoed by a gate that
reads only the transcript. With the detector on, the turn runs.

The rule (`summon.WithAcousticEvidence`): an utterance whose transcript does not
open with the name is still let through when all of these hold.

- The detector heard one of the agent's names.
- It heard it early: within 1.3 s of the start of the utterance by default
  (`Options.NameSummonAcousticWindow`; a negative value turns the rule off).
  The time counts from the start of the audio the recogniser is given, which
  begins `vad_pre_roll_ms` before the speech. The default was measured with
  the default 300 ms and moves with that setting; a window you set yourself is
  used as given.
- The transcript does not spell the name anywhere. If it does, the recogniser
  got the name right and its position already decided the matter, so "Tell me
  about Juno" stays vetoed.

What it lets through that the transcript gate would not:

- Anything the detector wrongly hears in the first word or two of an utterance.
  A homophone is heard as the name: "Juneau" matched "Juno" in every run below.
- A sentence that says the name there without addressing the agent, when the
  recogniser also misspells it ("Juneau is lovely in June").

The hit time is coarse. The detector reports about 0.3 to 0.6 s after the name
ends, in steps of about 0.3 s, so the window can tell "first word or two" from
"later" and nothing finer. Through voice detection with the default 300 ms of
pre-roll, "Juno, ..." and "Hey Juno, ..." reported by 0.8 to 1.2 s; "Okay
Juno, ...", "We flew to Juneau" and "Tell me about Juno" by 1.5 to 1.8 s; a name
further into a sentence by 3.1 s or later. The 1.3 s default therefore covers
one short filler before the name. With 1.6 s, "Okay Juno" is covered too, and
so is "We flew to Juneau last summer", which was then answered in all three
voices. "I think Juno is a great name" reported by 1.2 s in two voices and is
kept out only because the recogniser spelled "Juno" correctly.

Names other than "Obey" are written for the model from its own `bpe.model`, in
Go, with no Python at run time; 37 names were checked against the sentencepiece library's output.
The model's alphabet is A to Z and the apostrophe. A hyphen is read as a space.
A name with a digit or another letter ("R2D2", "Zoë") cannot be listened for:
`kws.CheckName` says so without needing the model, `voiceagent.New` logs each
alias it skips and returns an error if no name is left. The model is English and
hears a name the way an English speaker would read it.

Measured on Linux x86-64 with synthesized speech only, name "Juno", 16 sentences
in three Kokoro voices (two American, one British), 48 clips. The voice pack on
this machine is not the pinned one, so the clips are not checked in.

| Condition | Name heard / said | False matches / not said |
|---|---:|---:|
| Clean | 24 / 24 | 3 / 24 |
| Added white noise, 10 dB SNR | 20 / 24 | 3 / 24 |

All three false matches are "Juneau". "You know", "Do you know", "June" and
"Uno" did not match. All four misses in noise are the British voice.

The same 48 clean clips through the whole path (Silero, Parakeet `tdt-0.6b-v2`,
the detector, then a gate for "Juno"), comparing the transcript gate with the
gate that also takes the detector's word at the 1.3 s default:

| | Transcript only | With the detector |
|---|---:|---:|
| Addressed ("Juno, ...", "Hey Juno, ...", "Okay Juno, ...", "Juno."), answered | 13 / 15 | 15 / 15 |
| Not addressed, answered | 0 / 33 | 0 / 33 |

The two it rescued: "Juno." transcribed as "Did you know?", and "Hey Juno, set a
timer." transcribed as "Heijuno set a timer." Parakeet spelled "Juno" correctly
in every other clip that said it, which is why the mentions stayed out.

This says nothing about a real microphone, a real room or false activations per
hour. Measure your own name before shipping it:

```sh
go run ./scripts/kws_bench -model-dir /path/to/keyword-model \
  -names "Juno,the computer" -cases /path/to/cases.json
```

Each case may carry `"want_name"`, and each result reports `heard_by_s`, the
figure the gate compares with its window. For the whole path, including what
your recogniser makes of the name and what the gate then decides:

```sh
OBEY_KWS_STT_MODELS=/path/to/models OBEY_KWS_NAMES="Juno" \
  OBEY_KWS_CASES=/path/to/cases.json \
  go test -tags integration ./pkg/voiceagent/stt \
  -run '^TestParakeetNamedAcousticEvidenceNative$' -count=1 -v
```

Not covered: a recogniser other than Parakeet (a host that injects its own
recogniser gets the rule only if that recogniser fills
`stt.FinalTranscript.AcousticName` and `AcousticNameBy`), and a wake word that
fires while the user is still speaking. Detection still runs once per finished
utterance.

## Reproduce without microphone or playback

```sh
# Extracted keyword model directory; no user config is loaded by these checks.
OBEY_KWS_MODEL_DIR=/path/to/keyword-model \
  go test -race ./pkg/voiceagent/kws -count=1 -v
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
