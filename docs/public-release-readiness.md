# Public-source release readiness — 2026-11-04

Fixture follow-up merged in PR #341 at `22662c9c`. Owner source decisions were
recorded on 2026-11-04; their follow-up PR records the full candidate commit and
hosted checks. Security evidence below retains its explicitly stated audit scope.
This document does not approve changing visibility or rewriting shared history.

## Completed preparation

- Nine Apple System Voice recordings replaced with documented Kokoro synthesis.
  Two voiceflow recordings with undocumented origins also replaced. Exact prompts,
  model/voice/archive hashes, PCM hashes and WAV hashes are in
  [fixture provenance](audio/public-fixtures.json); reproducible generation uses
  explicit installed assets and no microphone, playback or personal configuration.
- Native KWS tests, native setup/cancellation tests and integration-tag voiceflow
  tests pass. The nine-case clean/noisy benchmark records its confusable false
  match rather than hiding it. Offline ASR checked generated content; it is not
  human listening, speaker-identity verification or speech-quality acceptance.
- Three remaining legacy benchmark workspace/cache paths were replaced with
  portable examples; structured comparison verified only path strings changed.
- The owner retains the short attributed GitLab transcript as a technical
  benchmark. README/NOTICE explain the purpose and unverified source license;
  the text, timings and derived measurements are unchanged.
- Ordinary project demos and documented test fixtures use proportionate source
  and privacy evidence. All original GIF sampling limits remain recorded; a
  complete-frame/listening sweep is not required by default. Real restrictions,
  imported voices and confirmed disclosures still need focused resolution.
- Isolated first-conversation software check passed all eleven checks: setup,
  settings persistence, missing-model/provider failure/recovery, text response,
  session persistence, restart/resume and an empty speech-model cache. Four
  macOS CLI notice-packaging tests passed. These do not imply a native-window,
  hardware audio, clean-machine installation or actual-inference pass.

## Refreshed security evidence

At audit baseline `15f4ae2b525d1ae8043f3dab8f7b6535ffddd297`, the read-only
origin mirror captured 343 advertised refs (main, seven tags and
335 PR heads), 2,116 reachable commit objects. Gitleaks 8.30.1 scanned 1,439 Git
commit diffs, reporting nine history pattern matches; the tree scan reported
three. All findings were checked: deterministic SHA-256 resume fingerprints or
one pinned model-file checksum. No exposed credential was confirmed in these
findings. Diff scanning is not inspection of every binary blob or external copy.
Final candidate changes require a final scan; this baseline is not silently
reused as a scan of later commits.

GitHub API observations: private repository, zero repository-level self-hosted
runners, read-only default workflow token, zero releases. Current CI uses hosted
Ubuntu runners, release jobs hosted Ubuntu/macOS. There are 54 unexpired Linux
Actions artifacts (about 1.56 GiB); they were inventoried, not downloaded or
executed. No actual disclosure in them was identified by this inventory.

Organization runner-group access returns 403 without `admin:org`; private-plan
branch/ruleset access is unavailable. Repository runner absence alone does not
prove absence of shared organization runners. Browser inspection was unavailable
in this session. Before public PR execution, an administrator must confirm the
[runner-group repository access](https://docs.github.com/en/actions/how-tos/manage-runners/self-hosted-runners/manage-access)
and intended public fork/branch policies. This is unverified configuration,
not a confirmed unsafe runner assignment. No policy was changed by the audit.

The current asset checker reports **89 approved records and 2 pending**. The
fifteen corporate variants are cleared by owner authority confirmation. Both
Veronica refs have confirmed synthetic-only inputs and matching offline ASR;
only provider output permission remains pending. These are two files in one
source family, not two confirmed violations.

## Remaining decisions and acceptance

1. **Veronica output terms:** input/source and targeted content review are complete.
   “Created with Grok” attribution is included. Resolve the applicable provider
   output permission for extracted-audio redistribution and Qwen reference use;
   [review evidence](media-review-evidence.md#veronica-reference-clips) records
   current and historical terms. No real-person input is asserted.
2. **Retained history:** the owner prefers keeping existing history. Continue with
   that publication strategy; no rewrite, remote deletion or publication has
   occurred. Old Apple WAVs remain reachable, so current fixture replacement does
   not settle their historical redistribution question. Resolve that specific
   issue and record dispositions for retained refs/outputs before publication;
   do not reopen the history-strategy choice absent a concrete need.
3. **CI:** the PR #343 corrections are included in this branch: a bounded
   debug-stream stress window plus deterministic blocked-write coverage, and a
   journal failure assertion synchronized with completed flush processing. The
   journal regression passes 100 race-enabled repetitions; debugstream,
   workjournal, transcript and events pass ten repetitions together. Updated
   hosted checks must still pass before treating CI as ready.
4. **Administrator settings:** verify shared runner access and public-fork policy
   when preparing visibility. Record any plan/visibility-dependent protection.
5. **Physical first conversation:** follow the existing
   [acceptance procedure](first-conversation-acceptance.md) on a disposable
   account/machine: fresh install/downloads, microphone denial/grant, audible
   reply, interruption and session relaunch. Record observed results. The
   deterministic software check is already done; these physical results are
   still unverified.

This is a source-release candidate. Developer ID signing/notarization and
separately distributed desktop/model bundles have their own release requirements;
the source-install documentation already identifies the development signature.
