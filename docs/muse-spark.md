# Muse Spark brain

Obey Voice uses [muse-spark-go v0.1.0](https://github.com/Obedience-Corp/muse-spark-go)
with the installed `muse` CLI. Install and authenticate Muse separately, then choose
`muse-spark` in **Settings → Brain**, or run:

```sh
obey-voice config brain_provider muse-spark
obey-voice config muse_spark_model <model-id>
```

An empty `muse_spark_model` uses Muse's default model. `MUSE_SPARK_MODEL` is the
environment override. Persona `brain.provider: muse-spark` and `brain.model`
overrides use the same model setting. Discovery lists the CLI default and any
configured model; the SDK does not provide model enumeration.

The adapter uses Muse's `meta` provider and streams `run.output.delta` events.
Reasoning, tool output, and task diagnostics never enter the speech stream.
A failed, cancelled, or missing terminal result returns an error and leaves
conversation history unchanged. Calls have no automatic retry.

Obey Voice keeps the complete transcript for persistence and replays a separate,
bounded working view each turn. `max_history` limits that view by whole
exchanges, without deleting earlier transcript turns. In a conversation shared
with other personas, that view is only the persona's share: see [Hosted
backends](personas.md#hosted-backends). Compaction replaces only the working
view with shared notes and a recent exchange; provider transfers preserve both
views and continuity metadata. There is no native Muse session resume. Clear,
load, transient compaction requests, and fresh worker jobs therefore cannot
inherit a stale native session. Persona and turn prompts resolve again at each
request boundary.

Conversational turns, route-only turns, and batch text transformations use the
empty preset with shell, write, web, reminders, and user-input dialogs disabled.
Worker turns with tools enabled use Muse's configured preset. Muse's sandbox
and permission profile remain active. Headless execution uses approval mode
`never`, so operations requiring approval fail instead of waiting for input.
Working directory and workspace both follow Obey Voice's configured working
folder; Muse worktree creation is disabled.

Cancellation terminates the SDK-owned CLI process. Remote model work and arbitrary
descendants are outside the adapter's observation. Token and cost accounting are
currently unknown to worker budgets. The SDK remains importable on all Obey Voice
targets; an unavailable Muse binary is reported at runtime.

For an isolated native echo check without inference credentials:

```sh
OBEY_VOICE_MUSE_ECHO=1 go test ./pkg/voiceagent/brain -run TestMuseSparkNativeEcho -count=1 -v
```
