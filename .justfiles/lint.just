set working-directory := '..'

[private]
default:
    @just --unsorted --list lint

# Run every read-only code quality check
all: justfiles fmt-check clippy go-vet

# Check Rust formatting without changing files
fmt-check:
    cargo fmt --all -- --check

# Apply Rust formatting
fmt:
    cargo fmt --all

# Check every Rust target with warnings denied
clippy:
    cargo clippy --locked --all-targets -- -D warnings

# Check the Go companion
go-vet:
    cd bridge && go vet ./...

# Check formatting of the root Justfile and every module
justfiles:
    #!/usr/bin/env sh
    set -eu
    for recipe_file in justfile .justfiles/*.just; do
        just --justfile "$recipe_file" --fmt --check
    done
