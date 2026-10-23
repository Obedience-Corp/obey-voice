#!/usr/bin/env just --justfile
# Obey Voice Agent — native Rust UI and shared Go voice runtime.

set dotenv-load := false

[doc('Development builds and local runs')]
mod dev '.justfiles/dev.just'

[doc('Formatting, Clippy, and Go vet')]
mod lint '.justfiles/lint.just'

[doc('Rust/Go tests and opt-in isolated UI fixture')]
mod test '.justfiles/test.just'

[doc('Local macOS bundles and signature verification')]
mod dist '.justfiles/dist.just'

[private]
default:
    @just --list --unsorted

# Build the Go companion and debug Rust app
build:
    just dev build

# Build the Go companion and optimized Rust app
build-release:
    just dev release

# Full local gate: lint, automated tests, and debug build (no UI or audio)
check:
    just lint all
    just test all
    just dev build

# Run the app with existing user config; additional arguments go to the app
[positional-arguments]
run *args:
    just -- dev run "$@"

# Run the visual preview with sample data, without a microphone
demo:
    just dev demo

# Build and verify a local debug macOS app bundle
bundle:
    just dist mac

# Build and verify a local optimized macOS app bundle
bundle-release:
    just dist mac-release

# Opt-in interactive UI test app with isolated config and mock model server
ui-fixture:
    just test ui

# Follow local desktop and voice-engine event logs
[positional-arguments]
logs *args:
    just -- dev logs "$@"
