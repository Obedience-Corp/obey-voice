# Obey daemon provider

Select `obey` as the brain provider to send conversations through
[obey-go-sdk](https://github.com/Obedience-Corp/obey-go-sdk). Install the Obey CLI
on `PATH`, start its daemon, and configure a provider and model in that daemon.
Voice does not install or start the daemon or copy provider credentials.

Configure the provider in Settings → Brain (terminal or desktop), or with:

```sh
obey-voice config set obey_provider YOUR_DAEMON_PROVIDER
obey-voice config set obey_model YOUR_DAEMON_MODEL
obey-voice config set obey_camp YOUR_CAMP
obey-voice config set obey_tool_policy daemon
obey-voice config set brain_provider obey
```

`obey_provider` is the daemon's provider identifier, which can differ from
Voice's direct provider names. `obey_model` is required; the SDK has no model
catalog API, so enter the configured model ID. `obey_camp` may be a name or ID;
when empty, the CLI infers the camp from Voice's resolved working directory.
`obey_socket` optionally selects a daemon socket. Empty uses the CLI default.
The corresponding environment variables are `OBEY_PROVIDER`, `OBEY_MODEL`,
`OBEY_CAMP`, `OBEY_SOCKET`, and `OBEY_TOOL_POLICY`.

## Tool policy

The default `obey_tool_policy: strict` refuses requests for tools-off behavior
before starting a run. This includes ordinary voice router turns and batch
transforms. The current SDK cannot enforce a universal tools-off switch across
daemon providers.

Set `obey_tool_policy: daemon` to delegate tool permissions to your daemon.
Voice requests discussion mode for conversational/batch turns and supervised
mode when its native tools gate is enabled. **Discussion mode can still use
tools, including reading files.** It is not a substitute for a daemon permission
policy. Voice never automatically approves an SDK approval event: it cancels
that run with an actionable error. CLI compatibility paths without approval
events remain subject to the caller's cancellation and CLI timeout.

## Conversations and workers

Voice owns the canonical transcript and bounded working context. Every request
uses a new SDK-owned run; the SDK attempts to stop that session afterward.
Subsequent turns replay the working context, including speaker/persona identity.
Compaction and persona changes therefore do not reuse hidden daemon history.
Failed and canceled turns leave Voice's stored history unchanged. Runs are not
retried automatically because daemon-side work may already have happened.

The SDK selects live streaming when an explicit camp and compatible CLI/daemon
capabilities are available. Its v1 compatibility path delivers the completed
answer in one chunk. Reasoning, diagnostics and tool output are not spoken.
Barge-in cancellation flows through the SDK and drains bounded CLI cleanup;
local process exit does not prove remote provider work has stopped.

Personas can select `brain.provider: obey` and their own `brain.model`. Workers
can select `voice_worker_provider: obey` and `voice_worker_model`; they inherit
the daemon provider, camp, socket, and tool policy, with separate history per
job. Batch transforms use the same daemon configuration without persona or
conversation history. Complete SDK token usage is reported; incomplete usage
and cost stay unknown to worker accounting.

## Verification

`go test -race ./pkg/voiceagent/brain ./pkg/voiceagent/config ./internal/persona
./internal/tui -run Obey` exercises streaming/final-only behavior, errors,
cancellation, history, persona/worker routing, and persisted terminal settings.
The transport fixture runs the actual pinned SDK against an isolated CLI
executable. It does not establish live daemon/model or microphone acceptance.
