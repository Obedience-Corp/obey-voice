# Publication candidate — 2026-11-05 UTC

Reviewed source commit: `3cbe9d9dae177cefc94d9d6d2f63c165baa20368`.
Repository: `Obedience-Corp/obey-voice`, still private. This record is not
publication approval and does not certify later commits automatically.

## Completed verification

- [Main CI run 41961806497](https://github.com/Obedience-Corp/obey-voice/actions/runs/41961806497)
  passed all six jobs. Three intermittent test failures in the final PR runs
  remain tracked; this success does not prove those failures repaired.
- Fresh origin mirror: 346 advertised refs (one branch, seven tags, 338 PR heads),
  2,124 reachable commits. Gitleaks 8.30.1 scanned 1,444 commit diffs and the
  candidate tree. Both scans exited 1 for findings: ten history matches and four
  tree matches. Finding rule/path/line/commit sets exactly match the previously
  triaged findings: deterministic resume fingerprints, pinned tokenizer/model
  asset hashes. No new credential finding was identified; no exclusions added.
- Candidate `git archive` SHA-256:
  `aad7c61e03bf169a02ed845e5a60b49ed3c6ba3ee9117f6b5d5cd33dd9e4dcd6`.
  This digest binds the reviewed commit, not a future commit containing this file.
- Authenticated browser inspection of organization Actions settings found one
  runner group. Its access summary explicitly excludes public repositories;
  its public-repository checkbox is unchecked. Two shared self-hosted runners
  are in that group. Repository API reports zero repository-level runners,
  read-only default workflow token and no workflow approval permission.
  No runner names or private machine details are needed in this public record.

## Narrow remaining choices

**Veronica — retained by owner decision:** both references are wholly
Imagine-generated without real-person input; offline ASR matches their phrases
and Grok attribution is present. The owner reviewed the provider-terms question
and explicitly chose to retain the exact references for public project use.
That closes this retention decision. No voice replacement or further provider
permission gate is planned. Source links and review limits remain in
[review evidence](media-review-evidence.md#veronica-reference-clips).

**Historical System Voice audio:** nine known Apple-generated WAV blobs from
baseline `15f4ae2b525d1ae8043f3dab8f7b6535ffddd297` total 500,384 bytes.
That baseline is an ancestor of current main, so replacing HEAD did not remove
those recordings from reachable history. Their known paths and Git blob IDs are:

| Path | Historical blob |
| --- | --- |
| `pkg/voiceagent/nativeaudio/assets/setup.wav` | `51547c9e671c4ba3e59e2b926e89e0e14f6a64bc` |
| `testdata/kws/bare.wav` | `32d08ff6b41d899d1115bf1d5b51cd895cd1dbe6` |
| `testdata/kws/positive.wav` | `272ea076ceed89a819803236ef1787e8ae960676` |
| `testdata/kws/british.wav` | `3d8aea84c7c9dba2399219d7e6a6868e6c43bcf8` |
| `testdata/kws/verb.wav` | `94f4b4e66edd3f48b3ea9b4b84dff331a37d633e` |
| `testdata/kws/mention.wav` | `e1340be114aa9501c2f5e06f86d6cc85974ae5a6` |
| `testdata/kws/negative.wav` | `265b2e302d0e7ed65804ecd97efdb340daab7880` |
| `testdata/kws/other-keyword.wav` | `487ee02cc86fe933b02521a3f827789b65ce3cfa` |
| `testdata/kws/confusable.wav` | `47cd98486d297c474333bf7d8ab1ebe7110caa78` |

This is a precise list of the nine reviewed baseline blobs, not a claim that every
historical recording/version has been reviewed. The current replacements are
Kokoro controls. The owner prefers retaining development history. Any proposed
historical cleanup must preserve that goal and show its exact effect before a
separate rewrite decision: filtering audio changes commit IDs and may break old
checkouts, and a force-push does not erase GitHub PR refs, artifacts or caches.
No rewrite, remote deletion or publication occurred.

## Final operational steps

1. Resolve the historical System Voice audio disposition above, preserving the
   owner's development-history preference. Current-tree media is fully cleared:
   92 records and zero pending after adding the isolated persona-list demo.
   Its tape/fixture and one-second samples were reviewed on 2026-11-05.
   Do not restart blanket media review.
2. Record the physical fresh-setup check: permissions, actual downloads/inference,
   audible response, interruption and session relaunch. Existing software fixtures
   passed but cannot establish these physical observations. Owner answer pending.
3. Dispose of retained external outputs. There are zero releases and 54 unexpired
   Linux Actions artifacts totaling 1,673,781,235 bytes. This is inventory evidence,
   not inspected archive clearance; no deletion is authorized by this record.
4. Public-fork approval and branch rules are unavailable through the current
   private-plan APIs. During the explicitly approved visibility transition, keep
   external workflows from executing until public fork approval and intended
   branch protection are verified, then enable the intended CI policy. Recheck
   runner exclusion and token permissions at that time. No settings changed here.
5. Refresh exact candidate/ref checks after any content or history change; obtain
   the concrete final visibility decision only after these dispositions are recorded.

Raw scan and mirror reports remain private. Pattern scanning is not a complete
binary, privacy, issue/discussion, Actions-log or external-copy audit.
