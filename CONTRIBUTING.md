# Contributing

Thanks for taking the time to improve Obey Voice.

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
expire after seven days. Tag builds attach archives directly to the GitHub release.

## Pull Requests

- Keep changes focused and describe the user-visible impact.
- Run `go test ./...` before opening a PR.
- Run `just build` when touching build, install, or runtime startup behavior.
- Include tests for behavior changes where practical.
- Do not include local config, model files, binaries, credentials, or generated review notes.
- Outside contributors accept the [CLA](CLA.md) once before their first contribution is merged. It lets Obedience Corp also use contributions in proprietary products; contributors keep ownership.

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

Outside contributions also require the [Contributor License Agreement](CLA.md).
You keep ownership; the CLA grants Obedience Corp additional rights to use,
modify, distribute, and sublicense your contribution, including in closed
company products. Those additional rights are not granted to all public users.
Third-party material keeps its original license and must be identified.

### Accepting the CLA

Accept once; the agreement covers your current and future contributions to this
project. Returning contributors do not need to sign or repeat the statement on
each PR.

Read [CLA.md](CLA.md), then post the following as a comment on your first pull request
from your own GitHub account. Replace the placeholders. Use the full commit SHA
of the `CLA.md` revision you read, not a mutable branch name.

```text
I have read and agree to the Obedience Corp Contributor License Agreement,
version 1.0, in CLA.md at commit <full commit SHA>, for my contributions
to this pull request and my future contributions to Obey Voice.
I understand that it permits Obedience Corp to use and sublicense my
contributions in proprietary products without releasing their source code.
I am accepting on my own behalf / on behalf of <organization's legal name>,
and I am authorized to grant these rights.
Signed: <your name>, GitHub @<username>, <YYYY-MM-DD>.
```

PR comments are public. If you need private acceptance, contact the repository
owner through a private channel before signing; maintainers must retain that
acceptance and reference its verification in the PR without publishing private
details. Do not post addresses or other unnecessary personal information.

Each outside contributor must accept for their own work. An authorized
organization representative may accept for contributions owned by that
organization. Identify coauthors and third-party material so maintainers can
verify all necessary rights. A DCO sign-off or checked template box alone is
not CLA acceptance.

Maintainers record the first acceptance with its version, revision, contributor
identity, represented organization (if any), and comment permalink or private
record reference. Reuse that record for later PRs; do not request another
signature or a fresh ownership review for every contribution. Company-owned
contributions do not require a separate CLA.

Revisit acceptance if the contributor reports a change in ownership or authority,
if a contribution includes work not covered by the record, or if new agreement
terms require acceptance. Contributors must identify third-party material and
notify maintainers when their existing authorization no longer covers the work.

Acceptance tracking is currently manual. No automated CLA check is configured.
