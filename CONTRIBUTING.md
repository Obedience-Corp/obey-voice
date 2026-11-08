# Contributing

Thanks for taking the time to improve Obey Voice.

First-time contributors accept the [CLA](CLA.md) through CLA Assistant.
It records acceptance and checks future PRs automatically. You retain ownership;
the public project uses AGPL-3.0-only, and Obedience Corp also receives rights to
use contributions in proprietary products.

## Development Setup

1. Install Go 1.26+.
2. Install `just`.
3. Clone the repository.
4. Run `go test ./...`.
5. Run `just build` for the project build workflow.

Voice-mode development requires local microphone and speaker access. Integration tests require Docker or a compatible container runtime.

CI builds and smoke-tests the relocatable Linux archive on every PR and main
push without uploading it to Actions storage. For downloadable Linux and macOS
builds, run **Release Artifacts** manually on the desired branch; those downloads
expire after seven days. Tag builds wait for both platforms, upload into a draft,
verify downloaded assets, then publish. See [release operations](docs/releases.md).
Native packaging requires Python 3.12+ and Bash 4+ in addition to the source
toolchain; these are builder requirements, not requirements for archive users.

## Pull Requests

- Keep changes focused and describe the user-visible impact.
- Run `go test ./...` before opening a PR.
- Run `just build` when touching build, install, or runtime startup behavior.
- Include tests for behavior changes where practical.
- Do not include local config, model files, binaries, credentials, or generated review notes.

### Agent / PR reject list (Qwen TTS)

**One-liner:** Product Qwen3-TTS is native-only (`qwen3-tts-worker` + GGUF under
`models_dir/qwen3-tts`); never reintroduce managed Python/uv/torch at inference.

Reject PRs that:

- Embed or ensure a Python `worker.py` / `qwen_worker.py` for product TTS
- Install or pin `uv`, torch, or `qwen-tts` for Obey Voice voice sessions
- Treat a legacy uv tree under `models_dir/qwen3-tts` as a ready product runtime
- Add a dual “Python until native URL is set” product fallback
- Claim Base-only cutover (0.6B + 1.7B tiers remain in scope for packaging)

Offline HF→GGUF conversion in the **lab** (`qwen3-tts-native`) may use Python;
that must not enter the Obey Voice process tree during synthesis. See
`docs/adr/0002-no-managed-python-tts-runtime.md` and `docs/qwen3-tts-spike.md`.

## Prompt maintenance

Store model instructions in named YAML documents under
`pkg/voiceagent/prompts/defaults/`, using the existing prompt schema and resolver.
Keep Go code responsible for runtime data and assembly, rather than inline
paragraphs of model instructions. Split independently editable concerns into
named documents or structured YAML sections. Document runtime placeholders and
verify user overrides, reload behavior, and malformed-edit fallback when changing
the loading path. See `docs/personas.md` for local overrides and inspection.
Agent Skills retain their standard `SKILL.md` format.

## Reporting Issues

When filing a bug, include:

- operating system and architecture
- Go version
- selected brain, STT, and TTS providers
- relevant command output
- whether microphone/speaker permissions were granted

## License

Obey Voice and contributions included in its public repository are licensed
under **GNU AGPL version 3 only (`AGPL-3.0-only`)**. See [LICENSE](LICENSE).

The [Contributor License Agreement](CLA.md) grants Obedience Corp additional
rights to use contributions in proprietary products. Contributors keep ownership;
those additional rights are not granted to all public users.

### One-time CLA acceptance

On your first PR, follow the CLA Assistant link and accept using your GitHub
account. The check updates automatically. Later PRs reuse that acceptance;
there is no signing comment, PDF, or maintainer acceptance checklist. A changed
agreement requires acceptance of its new version.

You must have authority to grant the rights in the agreement, including any
necessary employer permission. Identify third-party material and its license.
If ownership or your authority changes, tell the maintainers before contributing
work that your existing agreement does not cover.
