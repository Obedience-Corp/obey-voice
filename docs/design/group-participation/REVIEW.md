# Specification review

Self-review performed against current engine seams and recorded negative cases;
no independent reviewer or production implementation is implied.

| Concern | Resolution in the final spec | Required production proof |
| --- | --- | --- |
| First collective reply could duplicate the human turn, or all-pass lose it | Explicit admission persistence and `input_already_recorded` directive | Exact canonical/session counts on success/pass/refusal/failure |
| Initial reply could use tools because it is not a continuation | Explicit tools-disabled admission for every discussion phase | Worker/tool request counters remain zero |
| Selection alone might mutate state during a private pass | Candidate snapshot capability, then separate prepared commit | Unchanged binding/history/disk after check/pass |
| Successful inference can race new user input | Ticket checked after inference/prep, under commit, before output | Replay every fence case at real boundaries and race-enabled tests |
| Canonical text may exist before it is spoken | Delivered ledger separate from canonical working history | No coordinator reference to an incomplete/undelivered reply |
| Old no-repeat guard prevents real dialogue | Discussion-only returning-speaker policy; legacy guard retained | A→B→A plus per-person limits and old tests |
| Plural membership silently omits a persona | Frozen audience, explicit coverage and uncovered outcomes | Unseen omission remains a failing release case |
| Fast private JSON could still be wrong | Schema + host checks + semantic model gate, no confidence fiction | Both corpora and new blind cases on production prompts |
| Idle or reconnect could restart agents | No polling; ephemeral state; resume idle; snapshot reconciliation | Zero extra inference idle; restore/reconnect tests |
| Group result could compete with worker speech | Yield at response boundary to existing completion arbiter | Real ready-result and cancellation integration |
| Prototype timings could be mistaken for voice latency | Separate raw HTTP evidence, cache/workload limitations and unmeasured audio | Production first-audio/inter-speaker instrumentation |

Default audience/mode, passing/coverage, returning speakers, end conditions,
limits, interfaces, provider scope, settings migration and ownership are now
specified. No architecture placeholder is delegated to an unspecified “agent
decides” operation: model decisions have schemas, bounded inputs and explicit
host acceptance rules. Semantic correctness still requires measured model gates.

Review outcome: proceed to implementation behind disabled feature/capabilities.
Do not ship from this design evidence. The critical remaining technical issue is
model qualification; a valid JSON result cannot repair a misinterpreted audience.
