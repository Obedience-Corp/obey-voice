# Public asset review evidence — 2026-11-04

This follow-up starts at `15f4ae2b525d1ae8043f3dab8f7b6535ffddd297`.
The SHA-256-bound [inventory](media-review.json) records each retained asset's
review method and decision. It is evidence of a project review, not a warranty
of legal title or exhaustive content inspection.

## Proportionate review policy

The owner chose to retain ordinary project demos, documented synthetic test data
and the short attributed public-meeting benchmark. Use source inspection plus
targeted privacy review for these categories. A source gap alone is not proof
of infringement, and an audit should not invent per-file permission paperwork.
Specific restrictions, imported person/voice material, actual disclosures and
unexplained recordings require focused resolution.

A completed review means that the recorded method supports the retention decision.
It does not mean every GIF frame was inspected or a person listened to every WAV.
The earlier blanket requirement for complete GIF timelines and listening to all
synthetic fixtures has been removed. Every method and residual limit remains
explicit in the inventory. Actual byte changes still invalidate old digests.

## Remaining source decisions

- Two embedded Veronica references: owner source facts remain pending. Determine
  whether these were entirely Imagine-generated or used a real person's input,
  and which generation/input/output terms apply to redistribution and cloning.
- Fifteen corporate icon derivatives: source lineage is recorded in
  `apps/desktop/assets/README.md`; one owner confirmation of ownership/control
  and public-project authority covers the family.

These are two source families, not seventeen confirmed violations. The repo's
separate history, runner-control and install/physical-acceptance records remain
in the release checklist. Removing current files does not remove historical bytes.

## Replacement synthetic audio

[Public fixture provenance](audio/public-fixtures.json) records eleven WAVs:
nine replacements for macOS Samantha/Daniel synthesis, plus two replacements for
voiceflow recordings whose original source was undocumented. All are generated
locally with Kokoro's pinned multi-lang pack, `af_heart` or British `bm_george`,
speed 0.95. Their original test prompts are preserved; the voiceflow utterance is
explicitly “Hello Samantha.” and the barge-in control is “Okay.”

The source pack's [LICENSE](https://huggingface.co/csukuangfj/kokoro-multi-lang-v1_0/blob/main/LICENSE)
is Apache-2.0; [upstream usage](https://huggingface.co/hexgrad/Kokoro-82M) demonstrates
generated WAV output. No output-use restriction was identified in the reviewed
published terms. This supports routine synthetic fixture use; distributing model
weights separately still requires the relevant notices.

`just public-audio-fixtures /path/to/models` verifies model and voice hashes,
uses explicit existing assets, and produces canonical mono 16 kHz PCM16. It does
not download, read personal configuration, use a paid service, or open devices.
Native setup tests verify exact forwarding of the new canonical bytes; earlier
proof JSONs retain the original fixture hashes and were not rewritten as new data.

Offline Parakeet ASR checked the eleven generated files. Ten matched their intended
wording after punctuation/case/number normalization. British “Obey” was recognized
as “Oh bae,” with the remaining words correct. The actual native keyword detector
recognized that file correctly, along with the other enforced controls. Native
setup and voiceflow suites also pass. ASR is not human listening or identity proof.

[Keyword results](audio/public-fixtures-verification.json) cover all nine KWS cases
in clean and deterministic 10 dB noise. All six keyword cases match in both new
conditions. The confusable “Oh baby” falsely matches in both conditions; this is
reported rather than tuned away. Clean confusable audio also falsely matched in
the previous set. Changed recordings are not evidence of improved model accuracy.
Acoustic matching alone must never authorize a persona switch.

The former Apple recordings' source READMEs identified System Voice synthesis.
[macOS Tahoe SLA section 2F](https://www.apple.com/legal/sla/docs/macOSTahoe.pdf)
contains a specific public-redistribution restriction. The new current-tree fixtures
resolve that source issue; old copies remain in Git history until the owner chooses
a publication history strategy. No shared history was rewritten by this change.

## Existing synthetic and benchmark WAVs

Seven retained Kokoro outputs have adjacent generation text, pack and voice evidence.
Offline ASR matched their documented prompts with expected filler/number variation;
no unrelated speech was identified. Source evidence plus this targeted check supports
retention; no human-listening, voice-quality or training-data ownership pass is claimed.

The two audio-job files are LFM3 failed direct/staged reproduction outputs over
attributed AMI ES2002a input, distinct from the fixed-script narration proof.
ASR identifies the direct output as the documented summary sentence, while staged
output contains longer meeting speech. Their README/receipts describe failed
conversion honestly. Retain these short attributed technical outputs; do not claim
verified voice similarity, source-voice absence or production conversion quality.
AMI source/CC BY 4.0 attribution is recorded in the adjacent README and NOTICE.

## Project visuals

All 31 screenshot PNGs were individually inspected after PR #337's correction.
The missed variable-picker still was regenerated and independently verified as Alex;
its recipe now refreshes it with the GIF. The persona/settings captures use disposable
state and omit voice playback and host device lists. Original source fixtures and
synthetic geometric image attachments are recorded in demo scripts.

All 22 GIFs received eight evenly spaced frame samples. Nineteen unchanged GIFs
also received one-second and detected-scene samples checked against source recipes.
These support targeted content decisions, not an every-frame guarantee. The legacy
`tool-calls.gif` used a live model/filesystem; inspected samples show generic directory
names and responses, no confirmed private disclosure. Its future generation should
use a disposable filesystem/cache. This uncertainty alone is not a confirmed defect
in the retained bytes. Provider/persona UI labels do not imply bundled model assets.

## Public meeting excerpt

The owner explicitly chose to retain the 25 transcript records from the first
~90 seconds of GitLab Unfiltered's public 43-minute
[Product Marketing Meeting, 2021-06-28](https://www.youtube.com/watch?v=lBVtvOpU80Q).
This is a short technical speech/speaker-attribution benchmark. Source audio is
not committed; text/timing/derived measurements are unchanged. Attribution and
purpose are now explicit in `docs/benchmarks/t3b/README.md` and NOTICE.

No source license grant was verified. Retention records the owner's narrow-use
judgment and a plausible [fair-use rationale](https://www.copyright.gov/fair-use/),
not a claim that public availability confers a license or that the source is
relicensed under AGPL. No actual privacy or redistribution violation was identified.

## Veronica reference clips

The two Qwen refs identify Imagine clip names and text in adjacent metadata. Offline
ASR matches those reference phrases; it establishes neither a real person's identity
nor permission to use their voice. The source clips and generation receipts are absent.
The name `human-talk.mp4` is not evidence of a real performer.

Current [Grok Consumer Terms section 4](https://x.ai/legal/terms-of-service) addresses
input rights and output permission/attribution, referring to [Brand Guidelines](https://x.ai/legal/brand-guidelines).
Historical terms/account/feature and applicability to these extracted voice references
still need the source facts. Do not claim ownership language alone clears cloning.
No voice or persona has been removed or changed by this fixture work.

## Scope limits

The secret/history audit and software install smoke have separate exact-baseline
records. No microphone, speaker, voice-identity, clean-machine installation, downloaded
model license bundle or complete historical-media review is implied by this inventory.
No private audio was sent to external inference services. Machine-local raw ASR receipts
are retained privately; generation inputs and safe verification results are committed.
