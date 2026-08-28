# Audio8 voices

The Audio8 provider speaks by imitation. Its prompt carries a short piece of
reference audio — as codec frames — plus the transcript of that audio, and the
model continues in the same voice. A voice, to Audio8, is exactly that pair.

The export ships one: a Chinese reference recording and its transcript. English
spoken through it inherits that speaker's character. Registering your own
reference is how you change that.

## Consent

**Obey Voice ships no recording of any person, and never will.** There is
nothing built in to clone with. `voices register --wav` requires you to supply
the audio, and requires `--consent` alongside it — the flag is your statement
that you have the right to use that voice. Register someone else's voice only
with their permission, and expect to be able to show it.

`--from-kokoro` sidesteps the question: Kokoro synthesizes the reference line on
this computer and the codes are encoded from that. Nobody's voice is involved,
so `--consent` does not apply and the stored provenance records the audio as
synthetic. If you want an English Audio8 voice and have no recording to give,
this is the path.

Every registered voice records where it came from. `obey-voice voices
registered` shows it.

## Registering a voice

```bash
# No recording needed: Kokoro speaks the reference line here.
obey-voice voices register english --from-kokoro af_heart

# Your own recording, with the transcript the model needs.
obey-voice voices register mine --wav ref.wav \
    --text "exactly what is said in the recording" --consent

obey-voice voices registered            # what is registered, and where it came from
obey-voice voices registered --json     # the same, machine-readable
obey-voice voices remove mine           # delete one registered voice
```

`--from-kokoro` matches the Kokoro catalog case-insensitively but records the
catalog's own spelling, and refuses a name that is not in it. Kokoro resolves
its voice by exact name and otherwise falls back to `af_heart`, so a name it
cannot resolve would be synthesized in one voice and recorded as another.

Then select it, globally or per persona:

```bash
obey-voice config set audio8_voice english     # global default
obey-voice persona edit ada --tts-provider audio8 --voice english
```

`obey-voice render --voice <name>` selects a registered voice too, and the
render manifest and `--resume` key both record it — resuming a render after
changing `audio8_voice` starts over rather than splicing two voices into one
file.

Settings → Voice lists registered voices beside the packaged reference, and the
persona editor's voice row offers them too. `obey-voice voices preview --voice
english --provider audio8 --out sample.wav` auditions one without changing
anything.

### The reference audio

- **Any 16-bit PCM WAV**, up to the export's `max_audio_bytes` (50 MiB), which
  is checked on the file before it is opened. Stereo is downmixed and any sample
  rate is resampled to the model's 44.1 kHz with a band-limited filter.
  Registration happens once, so it uses a real anti-alias filter rather than the
  linear interpolation the playback path uses.
- **Between 0.5 and 30 seconds**, matching the vendor's own limit. Around ten
  seconds is the useful range.
- **The transcript must be exact.** It is fed to the model as what the reference
  audio says; a wrong transcript teaches the model the wrong mapping.

Longer is not better. The reference frames are prepended to *every* prompt, so a
30-second reference makes both the one-time prefix prime and every turn
afterwards slower, and eventually crowds the export's 2048-token context.

### Cost of switching voices

Audio8 primes the constant head of its prompt once and reuses it — about 1.0 s
of work that would otherwise be paid on every utterance
(`docs/benchmarks/A1-audio8-tts-onnx-2026-08-27.json`). The reference frames are
part of that primed state, so **the cache is per voice**: the first utterance in
a newly selected voice pays the prime again. Switching voices mid-conversation
is not free.

## Where voices are stored

```
models_dir/
  audio8-tts/                 # the export: usually a symlink into the HF cache
    registration/
      codec_encoder_fp16.onnx   # audio -> codec frames
      registration_manifest.json
  audio8-voices/              # the voice store, deliberately outside the export
    english/
      codes.npy               # [10][T] int64, the same format as the packaged reference
      voice.json              # name, transcript, locale, fingerprint, provenance
```

Registering a name that already exists replaces it by rename-aside, rename-in,
drop-aside: the previous voice stays on disk until the new one is installed, and
comes back if the install fails. If even that restore fails, the previous voice
is left in a `.<name>.old.*` directory and the error names the path — nothing
here deletes the only copy of a voice.

`voice.json` always records `consent`, including `"consent": false` for
synthesized references. An absent key would be indistinguishable from an
explicit no.

The store is a **sibling** of the export, not a directory inside it. The export
is normally a symlink tree into a read-only HuggingFace snapshot: a voice written
into it would fail on a read-only cache, and would be discarded by the next
snapshot fetch if it succeeded.

`models clean` claims `audio8-voices/` **whatever TTS provider is selected**,
unlike the export beside it, which is claimed only while audio8 is in use. The
asymmetry is deliberate: the export is 1.6 GB that can be downloaded again, while
a registered voice is a few kilobytes encoded from a recording you may no longer
have.

## Model revisions

Both manifests carry a `model_fingerprint`. Codes only mean anything to the model
revision that produced them — decoded against another one they are noise, not a
different voice. So the fingerprint is checked twice:

- **at registration**, between the registration package and the runtime manifest,
  before the encoder is opened;
- **at load**, between the stored voice and the installed export.

If you replace the export with a different revision, previously registered voices
are refused by name with both fingerprints in the message. Re-register them from
their source audio.

## Getting the export

Obey Voice does not download the Audio8 export; no manifest carries it and
`models ensure` installs nothing for it. Install the pinned snapshot under
`models_dir/audio8-tts` yourself — `obey-voice doctor` names the directory and
the snapshot, and reports how many voices are available once it is there.

Registration additionally needs the export's `registration/` package. Without it
audio8 still speaks in the packaged reference voice, and
`ProviderCapabilities.SupportsReferenceAudio` reports false.
