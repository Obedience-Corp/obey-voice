# Native context admission estimate

Native working evidence is not automatically dropped, archived, tool-shrunk or expanded to fit a context window. Before dispatch, the client rejects requests exceeding a conservative allowance; normal Standard context behavior stays unchanged. Requested Native context is capped at 32,768, matching the documented LFM3 window rather than the installed runtime's larger model-info value.

Admission counts text/tool JSON UTF-8 bytes individually, 128 units per message, one unit per 160 PCM samples plus 64 per recording. This deliberately exceeds the observed prefix/stride counts in the pinned fixture probes. It is an **estimate**, not a proven tokenizer bound or an exact token count. Overflow returns the existing typed prompt-budget error before HTTP and keeps previous history intact; the user can explicitly start a fresh context or provide a valid transcript/summary.

Tests exercise actual ThinkFull overflow with zero model requests and unchanged prior evidence, include audio geometry in admission, and cap a requested 128K window. Full brain race suite passes. The runtime's previously observed failure to enforce num_ctx remains unresolved: this estimate does not establish server-side no-omission behavior, model quality, or complete A acceptance. Actual ordinary-agent evidence and the reproducible runtime discrepancy remain separately recorded.
