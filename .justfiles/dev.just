# Development utilities (imported flat into root)

# Lint and format (golangci-lint enabled set lives in .golangci.yml)
lint:
    #!/usr/bin/env bash
    set -euo pipefail
    go fmt ./...
    go vet ./...
    if command -v golangci-lint >/dev/null 2>&1; then
        golangci-lint run ./...
    else
        echo "golangci-lint not installed — skipping (install: https://golangci-lint.run/welcome/install/)"
    fi

# Update and tidy dependencies
deps:
    go get -u ./...
    go mod tidy

# Watch for changes and rerun (requires entr)
watch:
    find . -name '*.go' | entr -r go run ./cmd/obey-voice

# Color recording of the real persona model list, using disposable Settings.
demo-persona-model-view: build
    #!/usr/bin/env bash
    set -euo pipefail
    mkdir -p demos/frames
    env -u NO_COLOR -u CLICOLOR CLICOLOR_FORCE=1 FORCE_COLOR=1 \
        TERM=xterm-256color COLORTERM=truecolor vhs demos/persona-model-view.tape
    # Extract settled frames from the recording; browser screenshots can catch a repaint.
    ffmpeg -y -loglevel error -ss 1 -i demos/persona-model-view.gif -frames:v 1 demos/frames/persona-model-view.png
    ffmpeg -y -loglevel error -ss 5 -i demos/persona-model-view.gif -frames:v 1 demos/frames/persona-model-inherited.png
    ffmpeg -y -loglevel error -ss 13 -i demos/persona-model-view.gif -frames:v 1 demos/frames/persona-model-claude.png
    ffmpeg -y -loglevel error -ss 18 -i demos/persona-model-view.gif -frames:v 1 demos/frames/persona-model-edit.png
    ls -lh demos/persona-model-view.gif
