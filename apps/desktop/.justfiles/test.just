set working-directory := '..'

# Default keeps `just test` compatible: Rust tests and isolated Go race tests
all: rust bridge

# Run Rust tests, optionally filtered by name
[positional-arguments]
rust filter="":
    cargo test --locked "$1"

# Run isolated Go companion tests with the race detector, optionally filtered
[positional-arguments]
bridge pattern=".":
    cd bridge && go test -race ./... -run "$1"

# Opt-in interactive macOS fixture; serves until Ctrl+C, never uses personal config
[macos]
ui:
    just dist mac
    python3 scripts/ui-fixture.py

# Isolated real reset/capture controls; no microphone or speaker playback
[macos]
audio-output:
    just dist mac
    python3 scripts/ui-fixture.py --audio-output

# Isolated persona preview UI: test tones, failure/retry, and cancellation
[macos]
persona-preview:
    just dist mac
    python3 scripts/ui-fixture.py --voice-preview

# Isolated protocol replay app: live transcript, notes, errors, and routing receipts
[macos]
events:
    just dist mac
    python3 scripts/event-fixture.py

# Replay active/queued work, completion, and inspection without providers or audio
[macos]
work:
    just dist mac
    python3 scripts/event-fixture.py --work

# Real routing adapter with temporary config, bundle, and a fake camp importer
routing:
    python3 scripts/test-routing.py

# Real companion setup, text reply, saved session and process restart; no audio/models
[positional-arguments]
first-conversation *args:
    python3 scripts/test-first-conversation.py "$@"

# Start native setup with no saved config; local test provider, no microphone automation
[macos]
setup:
    just dist mac
    python3 scripts/ui-fixture.py --fresh

# Fake audio/downloads for failure, cancellation, and retry checks in the native UI
[macos]
setup-replay:
    just dist mac
    python3 scripts/event-fixture.py --setup

# Optional descriptions: failed fetch, retry, and required-model readiness
[macos]
voice-catalog:
    just dist mac
    python3 scripts/event-fixture.py --voice-catalog

# Compile and test Linux adapters in a disposable container (requires Docker)
linux:
    docker build -f scripts/linux-check.Dockerfile -t obey-voice-agent-linux-check .
    docker run --rm -v "{{ justfile_directory() }}:/workspace:ro" -v obey-voice-agent-linux-target:/target -v obey-voice-agent-linux-cargo:/usr/local/cargo/registry obey-voice-agent-linux-check

# Image/GIF paste, send and resume with a local vision stub; no microphone or audio
[macos]
images:
    just dist mac
    python3 scripts/ui-fixture.py --images
