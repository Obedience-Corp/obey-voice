# VW0001 barge-in acceptance criterion

This repository-local excerpt records the campaign criterion used by the
[speaker echo design](../speaker-echo-rejection-design.md). Reviewers do not
need access to the private campaign repository to read it.

| Measure | Boundary | Initial acceptance target |
| --- | --- | --- |
| Barge-in | speech-start detection → playback stopped | no regression >50 ms against baseline; absolute result reported |

Measure elapsed durations using monotonic time. Report the absolute result and
compare at least 30 comparable recorded turns per baseline/worker condition,
with matching model, engine and hardware settings. Keep warm/cold samples and
shared/independent endpoints separate. Preserve failed samples.

The 50 ms allowance is a regression budget, not an absolute stimulus-to-stop
limit. Report stimulus-to-stop separately so waiting before detection remains
visible. The voice-response p95 regression target of 10% is a separate measure.
This document records an acceptance requirement; it does not claim that a test
enforces it or that the release gate has passed.

Provenance: My_Tools campaign, commit
`370362d536224373057c1ef109a4dcf4156957b6`,
`workflow/design/.dungeon/completed/2026-09-10/obey-voice-background-work-contract/EXECUTION-CONTRACT.md`,
section 6, “Barge-in” row. The row above is copied verbatim; the surrounding
guidance summarizes that section and the design's timing distinctions. This is
an excerpt, not a copy of the complete execution contract.
