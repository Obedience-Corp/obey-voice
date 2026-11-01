# Native context admission

Native evidence is not automatically dropped, archived, tool-shrunk or expanded to fit a context window. Admission rejects excessive complete requests before dispatch and preserves prior history. Client Native context stays capped at 32,768 with a ten-percent reserve.

The exact reviewed Ollama 0.40.12/model digest uses [pinned encoder geometry and a conservative rendered-text bound](context-contract.md). Validated candidate metadata selects this profile. Changed or unknown runtime/model identities use the older explicit estimate: UTF-8 text/tool JSON bytes, 128 units per message and one unit per 160 PCM samples plus 64 per recording. That general estimate is not a proven tokenizer bound.

The earlier num_ctx 2,048 discrepancy is now explained for the pinned MLX path: its actual model-level 128,000 limit rejects oversized complete expanded input. The client does not adopt that larger limit. Explicit truncate:false/shift:false and audio-only projection protection remain; no universal claim is made for unverified backend identities. Hardware and model-answer quality remain separate.
