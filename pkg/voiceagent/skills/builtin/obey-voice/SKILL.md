---
name: obey-voice
description: "Use and configure Obey Voice itself: personas, brain models, voices, settings, Calibre library access, ebook-to-audio conversion, document narration and audiobooks. Use for questions or actions about this voice assistant's own capabilities and setup."
---

# Obey Voice capabilities and configuration

You are running inside Obey Voice, a voice assistant with a terminal UI and persistent persona profiles. The current persona is one profile in that app; “create a persona for Obey Voice” means configuring the app, not inventing a character named Obey Voice.

Use the installed `obey-voice` CLI through your command tool. If the live conversation has only routing tools, send configuration actions to its worker. A chat response alone cannot create or change a profile. Ask for the persona's name or behavior when those are missing, then carry out the requested configuration when specified.

## Use built-in audio and library features

Obey Voice already creates audiobooks. Use `obey-voice audiobook create book.epub --out-dir out/book` (also digital PDF) for chapter/page WAVs plus a manifest; `--resume` skips unchanged completed sections. Inspect `audiobook create --help` for voices, formats and encoder requirements. `audiobook plan` and `audiobook review` prepare a production plan; the current `create` command renders the direct book input.

For Calibre books, inspect `calibre_enabled` and use `library list`, `library search QUERY`, or `library show ID`. With Calibre installed and enabled, `audiobook create --from-library QUERY --out-dir DIR` selects the source; convertible MOBI/AZW-family formats use Calibre's `ebook-convert`. Ask for a book or output location only if missing. Use `render` for text, Markdown, HTML, URL articles and EPUB; inspect `narrate --help` for PDF cleanup/planning workflows. Verify the output files/manifest before saying audio was generated.

Conversation participants already listed by the host are existing personas. Inspect them with `persona list/show` when a profile detail is needed; inviting Venessa or another participant to talk does not call for creating that persona. A separate skill is relevant only if its documented purpose matches the task.

## Inspect the installed interface

- `obey-voice --help` and `<command> --help` describe the installed version. Prefer these over remembered flags.
- `obey-voice persona list --json` lists installed personas. `persona show <id> --json` shows one profile; add `--with-prompt` for its prompt. Pass the id: with none, `persona show` reports the default persona for new conversations, which may not be the one speaking.
- A persona's brain provider and model, worker and voice live in its profile, not in app settings. Change them with `persona edit <id>` and its `--brain-provider`, `--brain-model`, `--work`, `--tts-provider`, `--voice` and `--tier` flags. `--work own` has the persona do its background work on its own backend and model; `--work worker` hands it to the configured worker.
- `obey-voice config get <key> --json` reads one app setting: its value, source, restart requirement and whether the default persona overrides it. `config schema <key> --json` describes one key: its type, accepted values, default and help. An unknown key returns the closest known keys. Without a key, both print every setting, more than a command result shows. Do not print or repeat secrets.
- `obey-voice doctor` diagnoses runtime readiness; `obey-voice models --help` and `voices --help` describe model and voice discovery.

## Make and verify requested changes

Create a persona with `obey-voice persona create "Display Name" --prompt-file <utf8-file> --no-activate --json`. The ID is derived from the name. `--no-activate` keeps creation separate from switching; use `persona use <id> --json` when the user requests changing the default persona. Inspect `persona edit --help` before editing an existing profile.

Use `obey-voice config set <key> <value> --json` for app settings and `config unset <key> --json` to restore inheritance. These validate the value, report what a rejected value should be, and preserve unrelated configuration. A persona's own model or voice wins over the app setting, so change persona settings with `persona edit`. Prefer owning persona/config commands over manually rewriting YAML.

Re-read the profile or setting after a write and report the actual saved value. Check provider/model/voice availability where relevant. A saved default does not prove an already-running conversation changed: use the current TUI's persona selector or a leading `@persona-id` message for in-session selection. Honor the command's restart requirement for settings that cannot reload live.

In the TUI, `/personas` manages profiles, `/settings` opens settings, `/menu` returns to the launcher, and leading `@` with Tab completes available personas. Keep spoken explanations brief and describe what was changed and verified.
