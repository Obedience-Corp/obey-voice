set working-directory := '..'

[private]
default:
    @just --unsorted --list dist

# Build, ad-hoc sign, and verify a local debug bundle (no installation or publishing)
[macos]
mac:
    just dev build
    python3 scripts/bundle.py
    just dist verify

# Build, ad-hoc sign, and verify an optimized local bundle
[macos]
mac-release:
    just dev release
    python3 scripts/bundle.py --release
    just dist verify

# Build and install an optimized app in ~/Applications, or a chosen directory
[macos]
[positional-arguments]
install directory="~/Applications": mac-release
    python3 scripts/install.py --directory "$1"

# Verify an existing local bundle without rebuilding it
[macos]
verify:
    codesign --verify --deep --strict "dist/Obey Voice Agent.app"
