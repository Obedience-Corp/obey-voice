# Actual local provider negative paths

The opt-in proof uses the installed local Ollama/LFM3 runtime through the actual prepared candidate and ordinary text pipeline. It does not start a daemon, install assets, open devices or read regular settings. It requires an absolute fresh receipt filename and a loopback endpoint:

```sh
just test native-persona-negative-proof /tmp/native-negative-fresh.json
```

[Receipt](evidence/native-provider-negatives.json), code `bcd4e1dd`: Ollama 0.40.12, LFM3 digest `19ff9eb6e621de5643e27d7a3d64e3fe62f9cd22d1471c9a34c01fb69b8e4f74`. The entire real run passed in 7.85 seconds. Quiet setup is recorded separately from the subsequent request trace.

- Invalid voice: one speech request returns HTTP 400, classified unsupported_input, zero samples. The synthesis request takes 2 ms; its subtest including provider/catalog preparation takes 3 seconds. No fallback or retry occurs.
- Already-canceled speech: zero HTTP requests.
- Cancel on the first actual chat delta: one delta, canceled result in 226 ms, zero speech requests and zero authorized output samples. No recovery speech.
- ASR cancellation: the real HTTP request was written, then canceled in 1 ms. This proves that transport boundary; it does not assert that GPU recognition had begun.
- Speech cancellation after the first PCM: 1,440 samples were privately observed, then producer join measured below 1 ms, with no playback engine attached.
- Full output queue: all eight batches were queued before cancellation. The producer joined below 1 ms; 20,640 already-buffered samples were privately drained afterward. Those bytes were never authorized for playback. A canceled stream need not erase bytes already queued before cancellation.

The trace records one maximum active HTTP body and zero bodies remaining after all producers join. Its middleware counts actual written requests and body Close, with a separate race-tested local HTTP regression for canceled dispatch and exactly-once close accounting. All owned candidate/provider resources close; the shared daemon is left untouched.

The speech-only milestones use raw software drains rather than claiming a physical playback fence. Existing race-tested pipeline queue/fence cases supply that boundary, including `TestRunTurnCancellationUnderFullQueueReturnsPromptly`, `TestCancelDropsSegmentsAboveThePlayingOne` and `TestBackgroundRevocationDuringPlaybackFencesLatePCM`. Shared speech tests cover malformed/truncated/stalled/429/5xx output; external Native configuration has no fallback or owned-runtime busy retry. This live receipt closes the real invalid-voice/producers subset, not listening quality, acoustic timing or the outstanding runtime context-accounting discrepancy.
