# Public asset review

The inventory in [media-review.json](media-review.json) records content and
redistribution decisions for tracked assets. A SHA-256 binds each record to its
current bytes; it does **not** establish ownership, consent, or license clearance.
The 2026-11-04 follow-up records owner confirmations, actual review methods and remaining actions in
[review evidence](media-review-evidence.md). Use the commands below for current
counts. No voice or persona was removed; adding an inventory entry alone does
not clear it.

## Commands

Run from the repository root:

```sh
just release media-check  # coverage, record structure and current-byte integrity
just release media-test   # isolated negative-path tests
just release media-ready  # same checks, plus every record must be cleared
```

The first command is suitable for ordinary development while review is in
progress. The last command intentionally fails until the public asset gate is
closed. Run it in a clean checkout of the exact publication candidate. These
commands do not publish, download, play audio or change inventory records.

Coverage includes tracked image/vector, audio, video, font, subtitle and PDF
extensions listed in `scripts/media_review.py`, plus the explicit t3b transcript
path. Any inventory entry, regardless of extension, is checked. Newly staged files
are included. Untracked/ignored files, content disguised with another extension,
arbitrary text transcripts, history, downloaded models and built archives need
separate review. A coverage pass is not a comprehensive asset or secret audit.

When an asset changes, review its new bytes and update the digest and relevant
evidence together. Do not automatically carry old clearance onto a new digest.
The checker verifies record presence and integrity, not the truth or legal
sufficiency of a review. To clear a retained asset, record:

- `public_release: "cleared"`, `disposition: "retain_public"`, and
  `content_review: "complete"`.
- A `review` object with `reviewer`, `reviewed_at`, `content_evidence`, and
  `rights_evidence`. Evidence should identify the permission/license and any
  relevant attribution, generation terms, consent and permitted cloning use.
  Reference private evidence without copying sensitive records into the repo.

Removed assets must leave the active inventory too; preserve their disposition
in the private release audit. Removing current files does not clear history.

## Provenance found and remaining decisions

| Asset group | Existing evidence | Work needed |
| --- | --- | --- |
| Veronica and VeronicaDesk reference WAVs | Owner confirms entirely Imagine-generated in their account without real-person input; offline ASR matches metadata text; `qwen/shipped.go` embeds both. | Owner explicitly elected public retention of the exact references after reviewing the provider-terms question. Both records cleared; attribution retained. |
| Native setup WAV | `pkg/voiceagent/nativeaudio/assets/README.md` records current Kokoro synthesis and links its pinned provenance; `setup.go` embeds it. | Replaced with pinned Kokoro output; canonical-byte forwarding and setup/cancellation checks pass. Historical copies remain a separate publication-history decision. |
| Acoustic keyword WAVs | `testdata/kws/README.md` records Kokoro generation, exact prompts and the British-accent control. | Eight Apple-voice samples replaced with pinned Kokoro controls. Offline ASR and native keyword tests recorded; all nine-case clean/noisy benchmark results are retained. |
| t3b transcript | `docs/benchmarks/t3b/README.md` describes 25 replayed meeting transcript records. Fixture docs name the public source video. | Owner chose short attributed technical benchmark retention; rationale and unverified source license are explicit. Source audio is not bundled. |
| Desktop logos/icons | All 15 variants trace to the companion app's corporate assets; visual content reviewed. | Owner confirmed corporate ownership/control and public project-use authority on 2026-11-04; all 15 records cleared. |
| Screenshots and GIFs | Visual inspection, source recipes and selective synthetic fixture regeneration are recorded per asset. | Follow each entry's actual coverage and decision; source review or sampled frames must not be described as every-frame inspection. |
| Golden/audio-job and voiceflow WAVs | Golden samples have Kokoro provenance and supported routine output terms. Audio-job receipts identify separate failed reproduction trials. Voiceflow now uses documented synthetic replacements. | Offline ASR/source checks recorded. Voiceflow recordings replaced with documented synthetic controls. Audio-job outputs retained as attributed failed-conversion evidence. |

The inventory includes the three desktop SVGs and embedded native setup WAV
previously omitted from the original 87-file list. Review updates preserve
asset identity and distinguish source evidence from verified audible content.

## Coordinated replacement/removal design

For shipped clone references, change the embedded asset declarations, voice
catalog and lookup, defaults, persona/config migration, picker behavior, tests
and documentation together. Existing saved voice IDs need an explicit usable
fallback; deleting WAVs alone breaks the build. Verify affected Qwen, TTS,
persona, configuration and UI paths with the project's test recipes.

For t3b, replace source audio and transcript together, regenerate attribution and
shift outputs, and label new measurements with the new corpus provenance. If
removing the corpus instead, update benchmark links and test expectations and
state the loss of historical reproducibility. Do not retain original derived
speech text while only renaming its source path.

For native setup audio, retain a finite deterministic recording with the setup
protocol's expected format and behavior; rerun native setup/cancellation tests.
New text/voice choices may require corresponding readiness assertions to change.

Downloaded voice/model packs and native runtime libraries are outside this
tracked-media inventory. Verify their exact versions, licenses, notices and
packaging separately for each distributed binary. The macOS CLI artifact now
copies and checks native notices through `scripts/package-macos-notices.sh`
(merged PR #334); its four negative-path tests pass. The separate desktop bundle
and any published model packs retain their own distribution requirements.
