# Asset review evidence — 2026-11-03

This review starts from `739320e336c530bbbe4a809b6ca5b9ef8b498969`.
The [inventory](media-review.json) binds decisions to individual file hashes.
Three GPT-6 Luna reviewers inspected visuals, audio source records, and
provenance; Codex consolidated their findings. This is a current-tree review.
It does not clear historical copies, downloaded models or release archives.

## Remaining publication actions

After independent verification, 31 screenshots are cleared and 60 records remain
blocked. The remaining records are grouped below; they are not 60 confirmed defects.
The original 52-clearance count was overstated: 21 GIF decisions relied on
sampling, and a cleared variable-picker still retained the owner's first name.
That still is now regenerated and verified; sampled GIFs remain pending complete
timeline review. All 31 retained screenshot PNGs were individually re-inspected.

| Group | Records | Next action |
| --- | ---: | --- |
| Corporate mark and icon derivatives | 15 | Confirm ownership/control and public project use once for the family. |
| macOS system-voice WAVs | 9 | Replace and verify affected behavior, unless a separate applicable redistribution grant exists. |
| Kokoro golden/KWS WAVs | 7 | Verify actual audio content; published output terms support routine use. |
| Veronica reference WAVs | 2 | Confirm generation/input provenance and applicable terms; review actual audio. |
| Audio-job WAVs | 2 | Review actual output against the direct/staged receipts and applicable source/model terms. |
| Voiceflow WAVs | 2 | Establish source and content, or replace with documented controls. |
| Meeting transcript | 1 | Resolve source permission or replace the corpus and regenerate dependent results. |
| Legacy tool-call GIF | 1 | Review the complete recording or regenerate with deterministic provider/filesystem fixtures. |
| Other GIFs | 21 | Complete the timeline review; preserve sampled/source evidence without treating it as final clearance. |

## Project UI captures

The captures depict this project's UI. Source recipes and fixtures are documented
in [demos/README.md](../demos/README.md). Product/provider names displayed as UI
labels are not imported model weights or third-party recordings. Ordinary
project-authored UI captures do not require separate permission for every file.

Visual inspection covered the 31 screenshot PNGs, 15 branding assets and samples
from every one of the 22 GIFs. The initial GIF pass used eight evenly spaced
frames per file, with follow-up on flagged scenes and their source tapes. This
is sampling, not an assertion that every animation frame was inspected. Individual
inventory entries state the review method and any remaining limitation.

The 19 unchanged GIFs received an additional one-second timeline sample and
scene-change sample (image-difference threshold 0.03), checked against source
recipes. Eighteen have supporting synthetic fixture/source evidence, but these
samples do not establish the contents of every retained scene. All GIFs remain
pending complete timeline review. `demos/tool-calls.gif` has an additional gap:
its tape uses live Ollama output, a real filesystem tool and an existing user
model cache. No private disclosure was found in its samples, but its source
does not support describing it as deterministic synthetic content. Review the
complete recording or regenerate it against a deterministic provider/filesystem.

The initial pass found the owner's first name in the persona form/model demos
and named host devices in the settings demo. These are reproducibility/privacy
issues in the demo fixtures, not exposed credentials. The three GIFs and five
persona stills were regenerated with a synthetic display name and disposable
working directory, omitting voice playback and the host device-list segment.
VHS navigation was updated for the current launcher and persona form. Reviewed
scenes include the prompt variable picker resolving the user name to Alex and
the model editor displaying only a temporary path. Source changes alone do not
clear old image bytes. Unrelated occurrences of first names in unit-test data
are outside this asset review.

Independent verification found that `persona-variable-picker.png` had been
mistakenly described as synthetic despite visibly retaining the owner's name.
It is now extracted from the corrected GIF's final variable-picker scene, where
the value is Alex. The demo recipe refreshes this still with the GIF. No other
additional privacy finding appeared in the individual screenshot recheck. This
review concerns publication content, not whether every legacy screenshot shows
the latest UI or accurately illustrates its filename.

## Corporate icons

[Asset provenance](../apps/desktop/assets/README.md) traces all 15 SVG/PNG
variants to the companion app's corporate mark and appiconset at revision
`77ad42ac4482ea54ea75511014b224587250797c`. This is one source family.
The visual review found branding rather than captured private content.
One owner confirmation of ownership/control and authority for public project
use closes the remaining source question; separate attestations per size are
unnecessary. Repository presence alone does not establish title to imported art.

## Kokoro synthetic audio

Seven WAVs have documented synthetic generation: the six
[`docs/audio/golden/{N2,N3,N6}`](audio/golden/) samples and
[`testdata/kws/kokoro.wav`](../testdata/kws/README.md). Golden provenance records
the text, `af_heart`, model SHA and `sherpa-multi-lang-v1_0` pack. The KWS README
records its command and text.

The [official sherpa documentation](https://github.com/k2-fsa/sherpa/blob/master/docs/source/onnx/tts/pretrained_models/kokoro.rst)
identifies the v1.0 source and voice mapping. The
[pack LICENSE](https://huggingface.co/csukuangfj/kokoro-multi-lang-v1_0/blob/main/LICENSE)
is Apache-2.0; the [upstream model card](https://huggingface.co/hexgrad/Kokoro-82M)
demonstrates writing generated WAV output with `af_heart`. No prohibition on
distributing these generated samples or output-specific attribution requirement
was found in those published terms. Routine output use is supported on that
basis; an undocumented training-data chain is not itself a demonstrated output
restriction. Model redistribution has its own notice obligations.

WAV metadata and source text were reviewed, but these files have not received an
audio listening pass in this audit. They remain blocked for actual content
verification. The golden files' existing subjective quality/listening checks
also remain unchanged; license review is not voice-quality acceptance.

## macOS system-voice fixtures

The [native setup README](../pkg/voiceagent/nativeaudio/assets/README.md) and
[KWS README](../testdata/kws/README.md) identify nine retained files as macOS
Samantha/Daniel synthesis: `setup.wav` and all eight KWS WAVs except `kokoro.wav`.
The current [macOS Tahoe SLA, section 2F](https://www.apple.com/legal/sla/docs/macOSTahoe.pdf)
restricts System Voices to personal, noncommercial projects and restricts
recording/publishing/redistribution in public-sharing and other contexts.
The exact generating system's accepted terms and any separate grant are not
recorded. [Apple's SLA page](https://www.apple.com/legal/sla/) cautions that
posted terms may differ from those accepted with a product.

The practical next step is replacement with appropriately licensed synthetic
speech unless a separate applicable permission already exists. Preserve the
spoken controls, PCM formats and setup contract; rerun acoustic-name behavior
and native setup/cancellation checks. Renaming the files or marking them
synthetic does not resolve this issue. No replacement has been claimed here.

## Veronica reference clips

The adjacent metadata identifies Imagine clips and reference text for the two
embedded Qwen samples. The source clips and generation receipts are absent.
The filename `human-talk.mp4` does not establish that a real person spoke.

Owner confirmation is needed about whether each clip was entirely generated
in the owner's Imagine account or used uploaded/third-party material, and about
the generation date/product. If a real person's recording or likeness was used,
the applicable permission must cover the intended cloning and redistribution.
The current [Grok Consumer Terms, section 4](https://x.ai/legal/terms-of-service)
(SpaceXSI, updated October 27, 2026) address content ownership and input rights,
but also expressly discuss permission and attribution when using Output and
refer to [Brand Guidelines](https://x.ai/legal/brand-guidelines). Those guidelines
specifically describe text/image attribution; they do not clearly resolve the
extracted audio/voice-reference case. Ownership wording alone is not a complete
output-use grant. Check the terms applicable to these clips and their intended
use, including permission/attribution requirements. Current pages do not prove
the historical account, terms or input provenance. Keep the
two references blocked until that specific source question and audio review
are resolved. Do not infer a performer identity from the persona name.

## Meeting transcript

All 25 t3b transcript records were read. The text is still derived from the
meeting identified in [fixture documentation](../tests/fixtures/meetings/README.md):
[Product Marketing Meeting (weekly), 2021-06-28](https://www.youtube.com/watch?v=lBVtvOpU80Q).
The review found no email addresses, phone numbers or URLs in the transcript;
that does not establish redistribution rights. Synthetic local paths did not
make the transcript, speaker timings or derived results synthetic.

Resolve the source license/permission, or replace the corpus and regenerate
dependent attribution/timing measurements with new provenance. The source
video's public availability alone is insufficient. The transcript remains
blocked pending that decision; related derived files need the same disposition.

## Other WAV evidence

The two [audio-job outputs](audio-jobs/evidence/README.md) are failed direct/staged
reproduction trials using AMI input and an isolated LFM3 runtime. Do not confuse
them with the separately described fixed-script narration trial. Their actual
audio content has not been checked in this audit. Preserve the documented AMI
attribution, verify each output against its receipt, and check the exact model
terms before clearance. The existing proof does not establish voice conversion
quality or source-voice absence.

Both voiceflow WAVs were introduced in commit `8fb92ef9`. Their test uses a fake
STT result, so the hard-coded result is not a transcript of `utterance.wav`.
The later T1 benchmark records that `barge_in.wav` says “Okay,” correcting an
earlier non-speech description. Neither record establishes source ownership or
the full audible content. Determine their source or replace them with documented
fixtures, then verify the affected voiceflow behavior.

## Review limits

Source-text review is not listening. No WAV was cleared by inspecting only a
header, filename or expected STT string. No personal speech configuration was
modified and no private audio was sent to an external transcription service.
Current online terms were checked on the review date; they are evidence about
the published terms, not historical account receipts or a legal adjudication.
