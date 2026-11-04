# Public-source release readiness — 2026-11-04

Review baseline: `15f4ae2b525d1ae8043f3dab8f7b6535ffddd297`. The follow-up
changes audio fixtures, attribution and recorded review decisions. The final PR
records the candidate commit and hosted checks. This document does not approve
changing visibility or rewriting shared history.

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

The read-only origin mirror captured 343 advertised refs (main, seven tags and
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

The current asset checker reports **74 approved records and 17 pending**: the
pending records are the fifteen corporate icon variants and two Veronica refs,
representing two source families. This is not a claim of seventeen defects.

## Remaining decisions and acceptance

1. **Veronica source facts:** two embedded cloning references remain pending
   owner generation/input facts and applicable redistribution/cloning/output
   terms. No voice was removed or presumed to be a real person's performance.
2. **Corporate mark authority:** fifteen variants share one documented corporate
   source family. One ownership/control and public-use confirmation covers them.
3. **Publication boundary:** current fixture replacement does not erase old Apple
   WAVs or earlier private-looking examples from Git history, PR refs, logs or
   artifacts. The owner must choose retained-history review/disposition or a
   reviewed clean public snapshot while keeping development history private.
   No historical deletion, rewrite or publication has occurred.
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
