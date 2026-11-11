# Codex

Obey Voice can use the installed Codex CLI for conversations, background workers,
and batch text processing. It uses the app-server protocol, tested with Codex CLI
0.163.0. Both the TUI and desktop runtime use this shared engine provider.

## Setup

Install Codex and sign in with `codex login`. Obey reuses that local login without
copying credentials or modifying Codex settings. In Settings → Brain, choose
**codex**, then a model and reasoning effort. Empty model/effort uses Codex's
default. Model discovery lists the CLI catalog; actual access depends on your
account. An incompatible model/effort returns the Codex error.

Equivalent configuration:

```yaml
brain_provider: codex
codex_model: ""                 # Codex default
codex_reasoning_effort: low      # empty uses Codex default
voice_worker_provider: codex
voice_worker_model: ""          # inherit the configured Codex model
```

Environment overrides: `CODEX_MODEL` and `CODEX_REASONING_EFFORT`. Changes apply to
new conversations; restart an already-running server. Persona `brain.provider`
and `brain.model` override the application provider/model, and the persona's
starting directory sets the process and tool workspace. Reasoning effort is an
application setting shared by Codex conversations and workers.

For tiered workers, select Codex with explicit subscription or metered billing and
`account_ref: default`. Cost enforcement remains subject to the existing capability
checks: the adapter cannot certify monetary charges or nested request counts.

## Conversation and tools

Only assistant message deltas go to speech. Reasoning and raw tool output are
excluded. Tool lifecycle events use the existing trace hooks. Logs include thread
and turn IDs, first-text timing and completion status without logging prompts or
credentials. Errors reach the existing conversation error surface and log sink.

Successful turns resume their Codex thread. Obey's transcript remains canonical:
loading history, host-added replies, prompt changes, errors and interruption drop
the cached thread, and the next turn uses the working conversation context.
Workers and transient summaries start independent threads. There is no automatic
inference retry after a failed turn.

Context has explicit limits. Native thread resume retains Codex's own context; a
reset, restored session, persona switch, or transient summary uses Obey's
working view: the last `max_history` user exchanges, with whole tool groups and
any leading compaction summary. Replay preserves historical speaker/persona
labels and tool call/result provenance. Unnamed historical assistants are not
renamed to the current persona. Compaction changes that working view without
replacing the durable transcript. In a conversation shared with other personas,
the working view is only the persona's share: see [Hosted
backends](personas.md#hosted-backends). Native tool details not represented in
Obey's transcript are not reconstructed after a reset.

Background workers start fresh with the existing task brief: up to eight recent
conversation turns within the shared brief byte budget, the triggering utterance,
and the configured persona, working directory and worker boundary. They do not
inherit the entire native Codex thread. Worker completion data is supplied through
the existing work-result context on the consuming turn.

Tools-disabled and live route-only turns disable configured MCP servers, plugins,
shell, web, apps, computer/browser and agent tools and use a read-only sandbox.
Tools-enabled turns use a workspace-write sandbox rooted at the starting directory,
with network escalation disabled. They never bypass the sandbox. Requests for
interactive approval or additional input are rejected with an actionable error;
complete those operations in Codex itself. A richer approval UI is not included.

Every operation starts and reaps an app-server process. Cancellation sends
`turn/interrupt`, then forcibly stops the local process group if necessary.
This observes local cleanup; it cannot prove remote work has stopped.
Codex stores resumable conversation threads in its own normal session store.

## Verification

```sh
just test codex
just test codex-live
just build && just test codex-ui
```

The first command uses a fake app-server subprocess and needs no login or audio.
The explicitly opt-in live command spends real inference turns using your Codex
login, in a temporary workspace, without microphone input or personal config edits.
It verifies streaming, second-turn recall and tools-disabled behavior and prints
first-text/total timings. The UI fixture selects and persists reasoning effort in
a disposable install; evidence is written under `/tmp/obey-codex-settings`.

Protocol reference: [Codex App Server](https://learn.chatgpt.com/docs/app-server).

Persona selection pins the persona document separately from the assembled
workspace instructions. Ordinary turns keep the persona chosen at conversation
startup. Explicit switches and settings/prompt reloads transfer the canonical
transcript, compacted working view, and continuity archive to a fresh Codex
thread; imported history invalidates the old thread without clearing continuity
metadata. Saved-session restore deliberately resets the previous session state.
The Codex test recipe includes the shared selector/pipeline path and verifies
response identity on disk, persona switches, reloads, and restored replay.
