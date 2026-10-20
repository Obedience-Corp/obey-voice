mod dev '.justfiles/dev.just'

build:
    just dev build

check:
    just dev check

test:
    just dev test

run: build
    ./target/debug/obey-voice-agent

demo: build
    ./target/debug/obey-voice-agent --demo

bundle: build
    python3 scripts/bundle.py

ui-fixture: bundle
    python3 scripts/ui-fixture.py
