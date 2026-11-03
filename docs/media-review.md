# Public asset review

The inventory in [media-review.json](media-review.json) records content and
redistribution decisions for tracked assets. A SHA-256 binds each record to its
current bytes; it does **not** establish ownership, consent, or license clearance.
All 91 records remain blocked at this review baseline. No voice or persona was
removed, and no asset was cleared by adding it to the inventory.

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
| Veronica and VeronicaDesk reference WAVs | Adjacent metadata identifies `imagine-clip`, source clip names and reference text; `qwen/shipped.go` embeds both. | Owner supplies generation/source and applicable redistribution/cloning permission evidence, or selects replacement/removal. Decide Her-inspired persona presentation separately. Metadata alone is not permission. |
| Native setup WAV | `pkg/voiceagent/nativeaudio/assets/README.md` identifies macOS Samantha synthesis, text, original and canonical hashes; `setup.go` embeds it. | Review the audio and applicable generated-audio distribution terms, or replace with a cleared equivalent that preserves the setup contract. |
| Acoustic keyword WAVs | `testdata/kws/README.md` identifies macOS Samantha/Daniel and Kokoro generation, commands and text. | Review each output and relevant voice/model/output permissions; synthetic origin alone does not close this gate. |
| t3b transcript | `docs/benchmarks/t3b/README.md` describes 25 replayed meeting transcript records. Fixture docs name the public source video. | Establish permission to redistribute the retained transcript and any intended source recording, or replace the corpus and regenerate dependent measurements. Public availability alone is not evidence of permission. |
| Desktop logos/icons | Three SVG sources were missing from the old inventory; PNG derivatives were listed. | Confirm source ownership and review both source and derived variants. |
| Screenshots, GIFs, golden/audio-job evidence | Existing inventory records selective synthetic fixture regeneration; golden audio has adjacent provenance. | Review retained content, fonts, voices, embedded source material and applicable permissions; preserve existing review notes without treating them as blanket clearance. |

Four missing files are now inventoried: the three desktop SVGs and embedded
native setup WAV. The 87 existing records retain their dispositions and notes.

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
tracked-media inventory. Inspect their exact versions, licenses, notices and
packaging separately for each distributed binary. In particular, the Linux
artifact recipe copies native notices, while the macOS CLI artifact recipe
currently copies binaries/libraries without an equivalent license-copy step;
this needs an artifact-specific follow-up before public binary distribution.
