# Inspect a TUI conversation pipeline

Start a conversation normally, then run `agent-stream-dbg` in a second terminal.
Choose **Live Obey Voice conversations** and select the current conversation.
The debugger opens its shared Flow, App, Timeline, Events and Messages panes.
Recording is optional. Both repositories need builds with live debugging support.

The TUI publishes recent diagnostic events in memory through a read-only,
authenticated loopback endpoint. Only private discovery metadata is written under
`<config-dir>/debug/runtimes`; no transcript or audio recording is started.
Slow/disconnected viewers cannot block the event bus. History is bounded to
1,024 records / 4 MiB, with sequence gaps exposing eviction; source queue losses
are reported separately. Clear/replacement closes the old feed and advertises a
new identity (checked every 250ms); reopen that conversation in the debugger.
Events during that transition may be excluded, so this feed is recent diagnostic
history rather than a durable audit. Exit removes discovery metadata. The
picker hides stale entries after a crash.

Use `1`–`5` for panes, `s` for turn/worker/session scopes, `[` / `]` for adjacent
turns, and `Ctrl+F` to follow latest. Enter expands Flow/App rows or opens source
details; `r` inside details shows raw evidence. `q` closes the viewer without
stopping the conversation. For a custom config location, press `c` in the live
picker and select the Obey Voice config directory.

### Save a conversation for later inspection

In a local terminal conversation, `/record talk` saves a playable
`conversation.wav` containing both microphone input and agent playback, alongside
events, source timing and conversation text. Microphone pause excludes input
audio; interrupted speech includes only the PCM submitted for playback. Pauses
and overlapping speech share one 48 kHz mono timeline, preserving playback bandwidth.
Recording notices include an **Open recording** link to the capture folder. With
mouse reporting enabled, the TUI opens it on click; otherwise your terminal handles
the hyperlink. `/record open` opens the latest capture even without terminal link
support, including after recording stops. `/record` shows the capture ID, link and recording
health. In terminals that support hyperlinks, use the terminal's link-opening
gesture to open the folder. `Ctrl+R` toggles Talk recording outside Vim mode;
`/record lab` includes the same conversation audio and additionally enables
existing audio debug artifacts. The manifest reports unavailable audio sources,
queue loss and recording errors; failed audio leaves the bundle incomplete.
Capture files are local, private to your user, and opt-in. Use `/record off` to seal the bundle.
Clear/new-runtime transitions detach recording; incomplete shutdowns remain
explicitly incomplete.

Open a second terminal with [agent-stream-dbg](https://github.com/Obedience-Corp/agent-stream-dbg):

```sh
agent-stream-dbg observe --dialect obey-voice --follow /absolute/capture/events.jsonl
# Inspect the same saved capture after recording stops:
agent-stream-dbg observe --dialect obey-voice /absolute/capture/events.jsonl
```

The viewer is passive: conversation/persona/work controls remain in Obey Voice.
It shows measured STT, admission, model, synthesis attempts and software playback
intervals, including overlaps, missing boundaries and interrupted stages. Select
an older turn with `[` / `]`, return to latest with `Ctrl+F`, or inspect raw and
normalized evidence with Enter. Background jobs retain their own typed job IDs
and causal parent turn. Viewer exit does not stop the conversation.

The source-owned [trace contract](../testdata/pipeline-trace/CONTRACT.md) and
[canonical fixtures](../testdata/pipeline-trace/README.md) define nullable clocks,
identity and compatibility. Legacy metrics remain relative; unknown schema
families/versions stay available as raw evidence. Playback timestamps mark
software sink acceptance/completion, not calibrated sound at the listener.

