# Pinned native context contract

The installed Ollama 0.40.12 MLX runner uses the model's hard 128,000-token context. The earlier num_ctx=2,048 probe exceeded that option because it is not the MLX runner's hard limit. This is now explained by source and actual rejection evidence; it is not evidence that historical audio was silently omitted. The client still caps Native admission at 32,768 and reserves ten percent. No expansion to the runtime's larger limit is introduced.

[Receipt and source hashes](evidence/context-contract.json) pin runtime, selected model digest, tokenizer/preprocessor assets and official source commit. The installed model reports MLX and lfm3-audio renderer. In that pinned path, media is expanded before the hard context check; oversized input rejects and generation is limited to remaining capacity. See the receipt's pipeline/media source links. This conclusion is specific to that identity, not all Ollama backends.

The encoder uses one frame below 400 samples; otherwise 1 +ceil((samples−400) / 160) frames, then ceil(frames / 8) audio tokens. Actual synthetic probes confirm the 1,520→1,521-sample boundary (one→two tokens), 16,000 samples→13 tokens and 480,000 samples→375 tokens. A two-recording request includes both expansions. A text input of 140,012 tokens rejects HTTP400; the same text with the 30-second recording rejects at 140,387, demonstrating expansion before rejection. No model output accuracy or recording-listening claim follows.

Production candidate preparation now passes its checked runtime/digest identity into the brain. Only the exact pinned pair selects this audio geometry and audited text upper bound. Other identities retain explicitly estimated admission. Text counts UTF-8 bytes because the pinned byte-level BPE has no normalizer, subword prefix or suffix; merges cannot increase the initial byte sequence. The bound separately counts literal renderer wrappers, roles, thinking, tool schema JSON and every tool-call function/parameter wrapper. JSON escaping overcounts raw string values. The shared lfm2JSON renderer adds spaces after JSON separators: every comma/colon is counted as one additional byte, including those inside strings conservatively. Tools also count an inserted system-turn envelope even without an initial system message. Counting mutually exclusive wrappers overestimates, and no rendered text or audio history is trimmed to make it fit. This is a conservative bound, not an exact text token count.

Tests cover identity mismatch, observed audio boundaries, tool-call parameter markup, thousand-element nested argument/schema separator expansion and an inserted system turn, refusal before HTTP without history loss, and the unchanged 32K ceiling. The earlier generic estimate's arbitrary per-recording allowance is not promoted to a proof. No tokenizer library, model installation or global settings are added.

Reproduce on the already installed pinned runtime with a fresh absolute receipt:

```sh
just test native-context-contract-proof /tmp/native-context-contract-new.json
```

The recipe uses only loopback HTTP, synthetic WAVs and a bounded one-token generation per small geometry probe. It creates the receipt exclusively, refuses another runtime/digest, and checks exact observed counts plus both hard rejections. It starts no daemon, opens no device and changes no configuration. The first ad-hoc mixed-input assertion expected two unnecessary wrapper tokens; the observed 140,387 correctly equals 140,012+ 375. The corrected published recipe passes all cases; the observation was retained.

Review caught omitted JSON separator spaces and the tools-only system envelope before publication. Those upper-bound gaps are repaired and covered by negative regressions; no observed token overflow was claimed from the draft.
