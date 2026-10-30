# Public release gates

The current-tree cleanup for WI-3200c0 does not approve publication. Keep the
repository and release artifacts private until the owner records these decisions
and the exact intended public refs and artifacts pass review.

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
- Review reachable history, deleted blobs, all refs, tags, releases, archives and
  external copies. A cleanup of HEAD does not remove historical disclosure.
  The owner must select a history strategy before any rewrite or publication.
- Scan the exact public tree, history and release artifacts for secrets; inspect
  findings privately and rotate any confirmed exposed credentials. A redacted
  current-directory scan is not a history or binary-content audit.
- Verify model, voice, font and other bundled third-party asset license obligations
  alongside `LICENSE` and `NOTICE`. Tests and metadata do not establish rights.

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
