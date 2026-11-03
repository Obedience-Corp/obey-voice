# Public release gates

The current-tree cleanup for WI-3200c0 does not approve publication. Keep the
repository and release artifacts private until the owner records these decisions
and the exact intended public refs and artifacts pass review.

Record each gate against a commit and artifact digest, with evidence and any
unverified conditions. Completing a design or merging cleanup changes does not
close a gate. Recheck changed content after the final merge.

## CI isolation

- Run untrusted PR code only on disposable, isolated runners. Verify the actual
  job runner assignment and token permissions; passing workflow lint is not enough.
- Verify repository, organization and enterprise runner access settings deny
  untrusted PRs access to trusted self-hosted machines. Changing workflow YAML
  alone cannot enforce this because a PR can request a different runner.
- Review fork token/secret policies and branch/tag protections. If a setting is
  unavailable while private, record the limitation and verify the intended policy
  at publication before allowing external workflows to execute.

## Assets and provenance

- Confirm the intended identity and behavior of the Her-inspired Veronica persona.
  The functional persona remains unchanged pending that decision.
- Establish source, redistribution and voice-cloning permission for both embedded
  reference clips in `pkg/voiceagent/qwen/voices/veronica/` and `veronicadesk/`.
  Metadata is provenance information, not evidence of permission. If uncleared,
  replace them or remove them together with catalog/default/migration changes.
- Establish source and redistribution permission for the t3b meeting transcript
  and source recording. Its local fixture path is now synthetic; the 25 transcript
  records and timing evidence remain original and are not cleared for publication.
- Review every retained media asset listed in [media disposition](media-review.json).
  Documented synthetic generation is not blanket distribution clearance. Unreviewed
  content is blocked for public release, including assets embedded in binaries.
- Verify model, voice, font and other bundled third-party asset license obligations
  alongside `LICENSE` and `NOTICE`. Tests and metadata do not establish rights.

## History and secrets

- Follow the [publication audit procedure](publication-audit.md). Select the exact
  repository, refs and artifacts to publish before scanning; record their hashes.
- Review reachable history, deleted blobs, all refs (including PR refs), tags,
  releases, Actions artifacts/logs, issues/PR discussions, archives and external
  copies. A cleanup of HEAD does not remove historical disclosure. The owner must
  select a history strategy before any rewrite, remote deletion or publication.
- Scan the exact public tree, history and release artifacts for secrets; inspect
  findings privately and rotate any confirmed exposed credentials. A redacted
  current-directory scan is not a history or binary-content audit. Retained media
  needs content review even when the scanner reports no credential findings.

## Fresh first conversation

- Verify the documented installation from a fresh checkout with disposable
  configuration and an initially empty model cache on a supported platform.
  Record prerequisites, provider/model versions, downloads and the exact commit.
- Verify microphone permission handling, a first audible response, interruption,
  persisted session and relaunch. Record hardware and observed results.
- Exercise missing provider/model, denied microphone and interrupted-download
  recovery. Keep personal configuration and recordings out of the test.
- Distinguish deterministic protocol/UI replay from actual model inference,
  physical audio and clean-machine installation. Open limitations remain gates;
  CI success and a simulated response cannot substitute for those observations.

## Benchmark redaction

B1–B1f JSON retains the original numerical measurements, gates, counters, sample
ordering and structure. `reply`, `spoken`, nonempty diagnostics and tool arguments
are replaced with explicit redaction markers. Local source/workspace paths are
placeholders. Character counters describe the original text, not the markers.
Historical qualitative claims cannot be independently reconstructed from redacted
text; rerun privately if raw text is needed for diagnosis. No synthetic reply is
presented as model evidence.

L1/L3/N3/N6/baseline and t3b input paths are synthetic replacements. Their numeric
measurements and retained transcript text are unchanged. See the
[benchmark sharing policy](benchmarks/README.md#sharing-artifacts).
