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
`stt.FinalTranscript.AcousticName` and `AcousticNameBy`). This rule runs once
per finished utterance; the next section is the detector on the live
microphone.

## A live wake word, for embedding programs

`Options.NameSummonLive` runs a second copy of the detector on the microphone
for as long as the agent lives. When it hears one of the name summon names it
emits `events.NameHeard`, a few tenths of a second after the name and before
the user has finished the sentence. That is the moment to chime or light up.

```go
agent, cleanup, err := voiceagent.New(ctx, voiceagent.Options{
    Config:         cfg, // name_summon_enabled and acoustic_name_enabled
    Events:         bus,
    NameSummonLive: true,
})
events.Subscribe(bus, func(e events.NameHeard) { chime() })
```

By default the cue changes nothing about which utterances are answered. The
gate still decides from the transcript and the per-utterance rule above. That
is deliberate: the live detector fires on the name wherever it stands ("tell me
about Juno") and on words that sound like it, so letting it open the gate would
answer all of those.

`Options.NameSummonWake` is the opt-in for the other behaviour: a wake word, a
pause, then the request. Each time the name is heard live the gate stays open
for that long, measured to the moment the request has been transcribed, so it
has to cover the pause and the sentence.

```go
NameSummonLive: true,
NameSummonWake: 8 * time.Second, // "Juno" ... "what's the weather"
```

With a wake window, the name said by itself ("Juno.", "Hey Juno?") is no
longer sent to the model. It opens the window, whether or not the live detector
caught it, so the request that follows is not spoken over a reply to the name.
A bare name the recogniser misspells is not recognised as one and is answered
as before. A request in the same breath ("Juno, what's the weather") is
answered as it always was.

What a wake window gives up: every mention of the name and every false match
opens the gate to that utterance and to anything anyone says for the rest of
the window. Without it the same sounds give a wrong chime and nothing else.

Limits:

- It needs `name_summon_enabled`, `acoustic_name_enabled`, a name the model can
  spell and a microphone (`New`'s own or an injected `Capture`). `New` returns
  an error otherwise, and when the keyword model is not in the models
  directory. `obey-voice models ensure --stt` installs that model only for
  `stt_provider: parakeet`. The live detector itself does not use the
  recogniser, but it has only been run next to Parakeet and next to an
  injected one.
- Names heard while the agent is playing a reply, and for one second after, are
  dropped, so the agent's own voice cannot cue or wake it. Saying the name over
  a reply therefore gives no cue; the recogniser still hears it.
- A conversation with a native-audio model (finalized audio input with
  conversation controls) does not consult the gate, so a wake window does
  nothing there, and native candidates refuse `acoustic_name_enabled`
  altogether. A program on that path can run `kws.Listener` itself, as below.
- The terminal app, the desktop app and `obey-voice serve` do not use it.

A program with its own audio loop can use the detector without `New`:

```go
listener, err := kws.NewListener(modelDir, []string{"Juno"})
defer listener.Close()
for samples := range microphone { // mono 16 kHz float32, off the audio callback
    hits, _ := listener.Write(samples)
    if len(hits) > 0 {
        chime()
        gate.Woke() // a summon.Gate built with summon.WithWake(8 * time.Second)
    }
}
// before sending a recording to a model that takes audio directly:
if gate.Awake() { ... }
```

Call `listener.Reset()` when audio was lost or the device changed.

Measured on Linux x86-64 (Ryzen 9 3900X) with synthesized speech only, through
`kws.Listener` in 20 ms chunks on one stream that was never reset:

| | Result |
|---|---:|
| "Juno" heard, 24 clips that say it (the set above, 2 s of quiet between clips) | 22 / 24 |
| The same 24 clips with a fresh stream per clip, for comparison | 24 / 24 |
| False matches in the 24 clips that do not say it | 3, all "Juneau" |
| False matches in 36.7 minutes of other speech, listening for "Juno" | 0 |
| False matches in the same speech, listening for "Obey" | 0 |
| Processor time per second of audio, one thread | about 2% |

The two misses were "Juno." by itself and a name in the middle of a long
sentence. A name that opens an utterance was reported 0.9 to 1.7 s after the
utterance began. Memory did not grow over 20 minutes of audio, and the stream
still heard the name afterwards.

The 36.7 minutes are 420 sentences of technical prose from this repository's
documentation in three Kokoro voices, with half a second between sentences.
No match in that much speech does not show a low rate: it is consistent with
anything up to about five false matches per hour of speech, and it says
nothing about names that resemble common words, about television, or about a
real room.

On a real microphone on the same machine the listener was run for ten seconds
in a quiet room: every callback was taken (10.03 s of audio, none dropped), it
gave no hit and it shut down cleanly. Nobody has spoken a name to it through a
microphone yet, on any machine.

To measure a name the same way, mark clips that should not match with
`"want_keyword": false`; they may be any length:

```sh
go run ./scripts/kws_bench -model-dir /path/to/keyword-model \
  -names "Juno" -cases /path/to/cases.json -live -gap 500ms
```

The report gives hits and misses, each hit's time from the start of its clip,
and `false_matches_per_speech_hour`.

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
