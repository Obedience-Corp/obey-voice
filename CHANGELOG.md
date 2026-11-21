# Changelog

## Unreleased

- Speaking agents can use their permitted tools directly by default. Existing `voice_router_tools: legacy` settings select the same behavior. New configuration writes use `direct`.
- Prompt files that startup copied into the prompts directory and you never edited now follow upgrades. Before, an install kept running the copy it was first given after the shipped prompt changed. Edited files are still never overwritten.
