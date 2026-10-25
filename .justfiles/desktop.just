set working-directory := '../apps/desktop'

[private]
default:
    @just --list --unsorted

# Lint, Rust and bridge tests, and build against this checkout's engine
check:
    just check

# Build the desktop and its companion
build:
    just build

# Run using the existing user configuration
[positional-arguments]
run *args:
    just -- run "$@"

# Visual sample data; no microphone or personal config
demo:
    just demo

# Build and sign a local macOS bundle
bundle:
    just bundle

# Build an optimized local macOS bundle
bundle-release:
    just bundle-release

# Build and install the optimized macOS app in ~/Applications
[macos]
[positional-arguments]
install directory="~/Applications":
    just -- install "$1"

# Forward focused checks or isolated fixture recipes, e.g. test bridge
[positional-arguments]
test *args:
    just -- test "$@"

# Opt-in native fixture with temporary config and a mock provider
ui-fixture:
    just ui-fixture

# Follow persistent application diagnostics
[positional-arguments]
logs *args:
    just -- logs "$@"
