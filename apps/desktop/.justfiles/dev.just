set working-directory := '..'

[private]
default:
    @just --unsorted --list dev

# Build the shared Go voice companion
bridge:
    mkdir -p bin
    cd bridge && go build -o ../bin/obey-voice-engine .

# Build the companion and debug desktop app
build: bridge
    cargo build --locked

# Build the companion and optimized desktop app
release: bridge
    cargo build --locked --release

# Run through Cargo so configured target directories are respected
[positional-arguments]
run *args: bridge
    cargo run --locked -- "$@"

# Visual preview only; never reads personal config or opens the microphone
demo: bridge
    cargo run --locked -- --demo

# Compatibility entry point for the original lint-only development check
check:
    just lint all

# Compatibility entry point for the automated test suite
test:
    just test all

# Follow persistent desktop diagnostics; use --demo for preview runs
[positional-arguments]
logs *args:
    python3 scripts/logs.py --follow "$@"
