# Changelog

## Unreleased

- A picture whose Markdown image reference ends a line of prose, such as `Here they are. ![A](</work/a.png>) ![B](</work/b.png>)`, is now attached. Before, only a reference on its own line was recognised, and a trailing one showed as raw Markdown with no picture and no warning. A missing or unusable file at the end of a line reports `[Image unavailable: …]` as an own-line reference does, and the prose before it is still spoken. A reference followed by other text on its line is left as written.
- `voice_router_tools: single_call` lets a routed Ollama turn answer chat in the routing request itself: plain text is the reply, and a `route` call hands the turn off. A chat turn is one streamed request instead of two. Personas can choose it with `brain.router`. Model requests in this mode are traced with the new `route_reply` purpose. The default is unchanged.
- Speaking agents can use their permitted tools directly by default. Existing `voice_router_tools: legacy` settings select the same behavior. New configuration writes use `direct`.
- Prompt files that startup copied into the prompts directory and you never edited now follow upgrades. Before, an install kept running the copy it was first given after the shipped prompt changed. Edited files are still never overwritten.
