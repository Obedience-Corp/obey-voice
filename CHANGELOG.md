# Changelog

## Unreleased

- `voice_router_tools: single_call` lets a routed Ollama turn answer chat in the routing request itself: plain text is the reply, and a `route` call hands the turn off. A chat turn is one streamed request instead of two. Personas can choose it with `brain.router`. Model requests in this mode are traced with the new `route_reply` purpose. The default is unchanged.
- Speaking agents can use their permitted tools directly by default. Existing `voice_router_tools: legacy` settings select the same behavior. New configuration writes use `direct`.
- Prompt files that startup copied into the prompts directory and you never edited now follow upgrades. Before, an install kept running the copy it was first given after the shipped prompt changed. Edited files are still never overwritten.
