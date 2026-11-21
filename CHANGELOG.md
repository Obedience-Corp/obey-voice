# Changelog

## Unreleased

- Typing `@` in the chat box lists paths under the campaign root (or the conversation's starting directory outside a campaign). Camp navigation shortcuts work, so `@p/` becomes `@projects/`. Tab accepts, Up/Down/Shift+Tab move, Esc closes the list, and Enter still sends the message as typed. At the start of a message personas and paths share one list. A message that starts with an `@path` such as `@projects/app/` now reaches the current persona instead of being refused as an unknown persona, and the model is told what `@path` is relative to.
- `voice_router_tools: single_call` lets a routed Ollama turn answer chat in the routing request itself: plain text is the reply, and a `route` call hands the turn off. A chat turn is one streamed request instead of two. Personas can choose it with `brain.router`. Model requests in this mode are traced with the new `route_reply` purpose. The default is unchanged.
- Speaking agents can use their permitted tools directly by default. Existing `voice_router_tools: legacy` settings select the same behavior. New configuration writes use `direct`.
- Prompt files that startup copied into the prompts directory and you never edited now follow upgrades. Before, an install kept running the copy it was first given after the shipped prompt changed. Edited files are still never overwritten.
