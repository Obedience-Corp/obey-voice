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
