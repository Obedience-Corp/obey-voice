# Public release gates

Current evidence and the focused remaining decisions are in
[public-source readiness](public-release-readiness.md).

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

- Owner retains the current Veronica personas and exact embedded references.
  Both references are entirely Imagine-generated in their account without
  real-person input; offline ASR matches the metadata and attribution is present.
  The owner reviewed the provider-terms question and chose public retention.
  This is a completed project decision; no replacement or additional provider
  permission gate is required by this checklist.
- Retain the short t3b excerpt as the owner's chosen attributed technical benchmark;
  its README and NOTICE record source, purpose, and the unverified license grant.
  The source recording is not bundled. No replacement is required by default.
- Review retained assets using proportionate source/privacy evidence in
  [media disposition](media-review.json). Documented synthetic fixtures and project
  demos do not require blanket every-frame or listening sweeps. Record the method,
  actual findings and limitations; resolve specific restrictions or imported voices.
- Nine Apple System Voice WAVs and two undocumented voiceflow recordings are now
  replaced with pinned Kokoro controls. Their old bytes remain in Git history;
  retained-history publication must resolve the specific historical redistribution
  question. The owner prefers preserving history; no rewrite is authorized.
- Verify model, voice, font and other bundled third-party asset license obligations
  alongside `LICENSE` and `NOTICE`. Tests and metadata do not establish rights.

## History and secrets

- Follow the [publication audit procedure](publication-audit.md). Select the exact
  repository, refs and artifacts to publish before scanning; record their hashes.
- Review reachable history, deleted blobs, all refs (including PR refs), tags,
  releases, Actions artifacts/logs, issues/PR discussions, archives and external
  copies. A cleanup of HEAD does not remove historical disclosure. The owner
  prefers keeping history; follow that strategy and record concrete findings
  and dispositions. Any rewrite, remote deletion or publication needs a separate
  explicit decision.
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
