# embed — a host application for the samantha voice agent

A small program that imports `pkg/voiceagent` and runs a turn.

It is a **separate Go module** with a `replace` directive pointing at a local
checkout. That is deliberate and load-bearing: compiling in-tree would still
succeed if the library secretly leaned on `internal/`, so only a separate module
proves importability.

```sh
go run .              # uses your real config
go run . -hermetic    # config built in code, every provider injected
```

## The hermetic gate

This is the acceptance criterion for the whole library extraction: **can a host
construct and run the agent with no samantha installation on the machine?**

It only means something run somewhere genuinely clean. Running as your own user
with `~/.obey` present proves nothing, because any leftover reach-in would find
the directory and silently succeed.

### Procedure

```sh
FAKEHOME=$(mktemp -d)
go build -o /tmp/embed-bin .
env -i HOME="$FAKEHOME" PATH=/usr/bin:/bin TMPDIR=/tmp /tmp/embed-bin -hermetic
find "$FAKEHOME"          # must still be empty
```

`env -i` clears the environment, so nothing is inherited — no `OBEY_VOICE_*`, no
`OLLAMA_MODEL`, no `XDG_*`. `-hermetic` states the config in code, injects the
brain and TTS, sets `Env` explicitly and points `PromptsDir` at a path that does
not exist.

### Result — 2026-08-11, passing

```
event: events.UserInput
event: events.ThinkingStarted
event: events.ResponseStreamingStarted
event: events.ResponseDelta
event: events.ThinkingComplete
event: events.ResponseReady
event: events.TurnMetrics
ok: agent constructed, ran a turn, and shut down cleanly
EXIT=0
```

The fake home was still empty afterwards: nothing was read from it and nothing
written to it.

Re-run this after any change to config resolution, provider construction or path
lookups. A failure here is a global reach-in, and the failure message will point
at the directory it wanted.

## Name summon

An agent with a name that answers only what is said to it, the way a
single-purpose voice assistant does:

```go
agent, cleanup, err := voiceagent.New(ctx, voiceagent.Options{
    Config: &config.Config{
        AgentName:         "Juno",
        NameSummonEnabled: true,
        NameSummonAliases: []string{"computer"},
        VoiceFrontend:     config.VoiceFrontendWebRTC, // needs a build with -tags apm; nlms does not
        // ... brain, speech recognition and voice settings
    },
    Events:             events.NewBus(),
    NameSummonFollowUp: 10 * time.Second,
})
// ...
for ctx.Err() == nil {
    if _, err := agent.RunTurn(ctx); err != nil { /* ... */ }
}
```

With the microphone open, a turn runs only when the utterance **starts** with
the name, its first word when it has several, or an alias, optionally after one
of `hey`, `ok`, `okay`, `hi`, `hello`, `yo`. Everything else is dropped before
the model sees it, with an `events.Info` naming what was ignored. The matching
rules are the ones described under
[Name summon](../../docs/usage.md#name-summon) and live in
`pkg/voiceagent/summon`.

```sh
go run . -summon      # scripted, no microphone, no models
```

| You need | Use |
|---|---|
| The gate | Set `name_summon_enabled`. `New` installs it; `agent.NameSummon()` returns it, or nil when it is off |
| A button, a tap or a held key to count as addressing the agent | `agent.NameSummon().SetBypass(true)` for that turn, then `false`. Safe to call when name summon is off |
| "yes" after the agent asks a question to count without the name | `Options.NameSummonFollowUp`. Off unless set. While the window is open, anything heard is answered, whoever said it, and each answer opens it again. It is measured to the moment the follow-up has been transcribed, so allow for the sentence itself |
| Other words in front of the name, or none | Build the gate yourself: `summon.NewGate(name, aliases, summon.WithFillers(...))`, set `Options.ExternalAddressing`, assign `agent.AddressGate = gate.Veto`, and call `gate.ReplyEnded()` if you use a follow-up window |
| A chime or a light the moment the name is spoken | `Options.NameSummonLive` with `acoustic_name_enabled`, then subscribe to `events.NameHeard`. A cue only: it does not change what is answered |
| "Juno", a pause, then the request | Add `Options.NameSummonWake`. Every mention of the name and every false match then opens the gate for that long |
| Your own rule entirely | Set `Options.ExternalAddressing` and assign `agent.AddressGate` |

`New` refuses to start, rather than start without the gate, when:

- **Nothing cancels echo.** `New` opens the microphone and plays audio itself
  and `voice_frontend` is `off`. The agent would hear its own name in its own
  voice and summon itself. If you inject `Options.Capture`, the audio path is
  yours and so is this check.
- **There is no name.** `agent_name` and `name_summon_aliases` are both empty.

Limits to know before building on it:

- The gate reads the finished transcript. The agent decides whether it was
  addressed after the speaker stops, and recognition runs on everything the
  microphone hears. For a cue the moment the name is spoken, set
  `Options.NameSummonLive` and subscribe to `events.NameHeard`; for "name,
  pause, request", add `Options.NameSummonWake`. Both need
  `acoustic_name_enabled`, and the wake window answers more than the
  transcript gate would: see
  [a live wake word](../../docs/voice/acoustic-obey.md#a-live-wake-word-for-embedding-programs).
- A name the recogniser misspells is not matched. Pick a name it transcribes
  reliably, and add the misspellings you see as aliases. With Parakeet you can
  also set `acoustic_name_enabled`: a keyword model then listens for the same
  names, and an utterance that opens with a misspelled name is let through.
  It also lets through what that model mishears at the start of an utterance,
  so read [the measurements](../../docs/voice/acoustic-obey.md#other-names-for-embedding-programs)
  and measure your own name first.
- Typed turns (`SendText`) are never gated.
- With finalized audio input (`Options.Input` or `InputFactory`) the gate
  depends on the native policy the host binds. In conversation mode it is not
  consulted at all: the model receives the audio and name summon does nothing.
  In standard mode it reads the transcript made for spoken controls. This path
  has no test with name summon yet.
- If you set `AdmitInput` to route between personas, that owns addressing; set
  `Options.ExternalAddressing` and do not use both.

## Known limits

**The mechanical `grep -rn 'os.Exit\|os.Getenv' pkg/` check does not return
nothing**, and cannot without breaking documented behaviour. Twelve reads remain:

| Where | Why it stays |
|---|---|
| `config` | `BRAIN_PROVIDER`, `VOICE_TOOLS_ENABLED`, `SKILLS_ENABLED`, `OBEY_VOICE_SYS_MEM_BYTES` — the documented env-var configuration contract |
| `qwen` | `OBEY_VOICE_QWEN_NATIVE_URL`, `QWEN_TTS_NATIVE_SHA256` and friends |
| `brain`, `prompts` | `USER`, reached **only** when the caller supplied no `Env` or placeholder value |

The intent behind that check — the library must not reach for process state
behind the caller's back — is what the hermetic run above actually verifies, and
it verifies it directly rather than by proxy. `os.Exit` in `pkg/` is **zero**,
which is the half that matters for embedding: a library must never terminate its
host.

**Without `-hermetic`** the example needs a working brain provider. With none
configured it exits with `init brain: ollama_model not configured — run: samantha
config ollama_model <model>`, which is the error path behaving.
