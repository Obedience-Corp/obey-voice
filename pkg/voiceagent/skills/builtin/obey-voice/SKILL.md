---
name: obey-voice
description: Help configure Obey Voice itself, including creating and editing personas, selecting brain models and voices, and diagnosing app settings. Use when a user asks this voice assistant to change or explain its own setup.
---

# Obey Voice configuration

You are running inside Obey Voice, a voice assistant with a terminal UI and persistent persona profiles. The current persona is one profile in that app; “create a persona for Obey Voice” means configuring the app, not inventing a character named Obey Voice.

Use the installed `obey-voice` CLI through your command tool. If the live conversation has only routing tools, send configuration actions to its worker. A chat response alone cannot create or change a profile. Ask for the persona's name or behavior when those are missing, then carry out the requested configuration when specified.

## Inspect the installed interface

- `obey-voice --help` and `<command> --help` describe the installed version. Prefer these over remembered flags.
- `obey-voice persona list --json` lists installed personas. Use `persona show <id> --with-prompt --json` to inspect a specific profile and its prompt.
- `obey-voice config get --json` and `config schema --json` report current settings, valid keys, ownership and restart requirements. Do not print or repeat secrets.
- `obey-voice doctor` diagnoses runtime readiness; `obey-voice models --help` and `voices --help` describe model and voice discovery.

## Make and verify requested changes

Create a persona with `obey-voice persona create "Display Name" --prompt-file <utf8-file> --no-activate --json`. The ID is derived from the name. `--no-activate` keeps creation separate from switching; use `persona use <id> --json` when the user requests changing the default persona. Inspect `persona edit --help` before editing an existing profile.

Use `obey-voice config set <key> <value> --json` for app settings and `config unset <key> --json` to restore inheritance. These validate types and preserve unrelated configuration. Persona overrides can take precedence over app defaults; inspect the profile before changing a global setting that it overrides. Prefer owning persona/config commands over manually rewriting YAML.

Re-read the profile or setting after a write and report the actual saved value. Check provider/model/voice availability where relevant. A saved default does not prove an already-running conversation changed: use the current TUI's persona selector or a leading `@persona-id` message for in-session selection. Honor the command's restart requirement for settings that cannot reload live.

In the TUI, `/personas` manages profiles, `/settings` opens settings, `/menu` returns to the launcher, and leading `@` with Tab completes available personas. Keep spoken explanations brief and describe what was changed and verified.
