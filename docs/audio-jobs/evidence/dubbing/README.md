# Reviewed dubbing transport proof

WI-43b072. The production source is `d297f08d`; the build includes the final
review-capsule, language and sampling validation in that commit. The exact worker
SHA, input/output hashes, runtime/model/projector digests, source intervals,
approved scripts, original translation JSON and all four manifests are in
[receipt.json](receipt.json). The JSONL event files retain each terminal result.

The new finite routes are English/Spanish translation drafts (v2) and explicit
reviewed-script dubbing previews (v3). Each is a separate job. Recording-preserving
voice conversion remains unavailable. The [protocol](../../../audio-job-protocol.md)
specifies review binding, compatibility, languages and duration handling.

## Actual runtime result

The input is the same licensed human AMI ES2002a channel 0 recording and verified
2–12s slice described in [audio-input.md](../audio-input.md), corresponding to
132–142s of the original meeting. It has 160,000 samples at 16 kHz and SHA
`4575ad9debf4735e4eb9c4684d99d2baa92effbb9c650edacd30c44115c14465`.
The [AMI corpus](https://groups.inf.ed.ac.uk/ami/corpus/) attribution and CC BY 4.0
terms remain applicable. The reverse path uses generated Spanish, not an
independent human Spanish fixture.

The owned b11960 runtime and already installed LFM3 Q4_K_M/projector pack ran
under the existing loopback-only sandbox. There was no download, assistant,
microphone or speaker. Each worker exited 0 after accepted/progress/artifact/
completed; no owned runtime scratch or lock survived. Independent inspection
recomputed every input and output hash after the run.

| Job | Input duration | Output |
|---|---:|---|
| English→Spanish translation | 10.00s | Spanish text draft; 152 prompt tokens, 48 completion tokens |
| Reviewed Spanish speech | 10.00s source | 249,600 samples at 24 kHz = 10.40s |
| Spanish→English translation | 10.40s generated fixture | English text draft; 157 prompt tokens, 37 completion tokens |
| Reviewed English speech | 10.40s source | 245,760 samples at 24 kHz = 10.24s |

The Spanish preview is 0.40s longer than the original. The worker keeps this
mismatch; it does not pad/trim/stretch speech or alter picture timing. The editor
must show durations and reject an unsupported Apply policy. An unchanged final
binary produced the same hashes in repeated local runs, but future regeneration
is not promised bit-identical.

The harness explicitly asserts fixture-only review of the translation draft.
It is not evidence of a user reviewing a rendered editor control. Both scripts
and the complete review capsule survived JSON request/manifest round trips. The
reverse model text changes the final question “¿Tienes lo mismo?” into a statement
about the others being the same; this is a visible wording/meaning change, not a
linguistic-quality pass. No independent listener, pronunciation, voice similarity,
word alignment, seam or language quality qualification is claimed. All task
qualification flags remain false.

## Verification and reproduction

`just build`, `just test audio-jobs` under the race detector, and scoped
`golangci-lint` pass. Tests cover both language directions, exact source PCM and
all overlapping windows; fixed prompts/no tools; unsupported/injected language
choices; stale source/range/revision; corrupt or missing review; JSON byte
preservation; relocated input/cache removal; script/target-language dispatch
without mutating English narration; output limits, cancellation, provider failure
and source mutation without a published take. Forged huge source counts reject
before allocating a window plan. Existing v1/v2 checks remain in the recipe.

```sh
just test dubbing-proof /absolute/worker /absolute/config.json \
  /absolute/english-to-spanish-request.json /new/private/output \
  docs/audio-jobs/evidence/loopback-only.sb
```

The request is a complete v2 translation envelope with the intended immutable
English input. The proof requires exactly one window; it never concatenates
review-overlap text into a script. The managed config points to existing verified
assets. The harness creates a fresh private root, runs four actual finite jobs,
resamples generated Spanish to 16 kHz for the reverse transport check, and saves
its receipt. Timeout kills only the worker's newly owned process group.

The source-caption PR and narration take importer do not yet consume v3. Next
editor work is language selection, editable translation review, explicit Generate,
duration comparison, source/revision fencing and durable take import/playback/
export. This backend proof does not complete editor E03/E08/E12 or the full design.
