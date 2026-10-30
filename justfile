#!/usr/bin/env just --justfile
# Obey Voice — ultra-low-latency voice assistant for AI coding (engine; formerly samantha)

set dotenv-load := true

BUILDTOOL := "go run ./internal/buildutil"
binary_name := "obey-voice"
bin_dir := "bin"
gobin := env_var_or_default("GOBIN", `go env GOPATH` + "/bin")

# Modules
[doc('Native desktop builds, checks, and isolated UI fixtures')]
mod desktop '.justfiles/desktop.just'

[doc('Testing (unit, integration, dashboard)')]
mod test '.justfiles/test.just'

[doc('Voice & audio setup')]
mod voice '.justfiles/voice.just'

[doc('Release (tag, publish, formula sha)')]
mod release '.justfiles/release.just'

[doc('Latency benchmarks and the recorded utterance corpus')]
mod bench '.justfiles/bench.just'

[doc('Opt-in local conversation flow (real Ollama live+worker, kept artifacts)')]
mod convoflow '.justfiles/convoflow.just'

# Flat dev utilities
import '.justfiles/dev.just'

[private]
default:
    #!/usr/bin/env bash
    echo "Obey Voice — Give Claude a voice (Go)"
    echo ""
    just --list --unsorted

# Build (vet + compile via dashboard)
build:
    @{{BUILDTOOL}} build

# Color README walkthrough with disposable data and scripted voice activity.
demo: demo-readme

# Original live Ollama/tool-call recording (requires an installed local model).
demo-tool-calls: build
    #!/usr/bin/env bash
    set -euo pipefail
    vhs demos/tool-calls.tape
    just _optimize-demo-gif demos/tool-calls.gif

# Voice meter animation demo (listening / hearing / speaking).
# Matches festival/termcast tapes: shell-exported color env, raw VHS GIF.
# Important: clear NO_COLOR from the parent env — agent/CI shells often set it
# and lipgloss then emits monochrome frames.
demo-voice-meter: build
    #!/usr/bin/env bash
    set -euo pipefail
    env -u NO_COLOR -u CLICOLOR \
        CLICOLOR_FORCE=1 FORCE_COLOR=1 \
        TERM=xterm-256color COLORTERM=truecolor \
        vhs demos/voice-meter.tape
    ls -lh demos/voice-meter.gif

# Managed Qwen provider selection + nine model-native voices in Settings.
# Uses a disposable completion-marker fixture; no model download is required.
demo-qwen-voices: build
    #!/usr/bin/env bash
    set -euo pipefail
    env -u NO_COLOR -u CLICOLOR \
        CLICOLOR_FORCE=1 FORCE_COLOR=1 \
        TERM=xterm-256color COLORTERM=truecolor \
        vhs demos/qwen-voices.tape
    just _optimize-demo-gif demos/qwen-voices.gif
    ls -lh demos/qwen-voices.gif

# Settings walkthrough: the sidebar of sections, lists drawn whole, choice
# rows, the volume slider. Fake Qwen fixture; no download.
demo-settings-sidebar: build
    #!/usr/bin/env bash
    set -euo pipefail
    env -u NO_COLOR -u CLICOLOR \
        CLICOLOR_FORCE=1 FORCE_COLOR=1 \
        TERM=xterm-256color COLORTERM=truecolor \
        vhs demos/settings-sidebar.tape
    just _optimize-demo-gif demos/settings-sidebar.gif
    ls -lh demos/settings-sidebar.gif

# Conversation TUI: live thinking, then Ctrl+K to keep it in the thread.
# Scripts bus events (no live mic/model). Unsets NO_COLOR so VHS paints color.
# Conversation TUI: /record talk seals events.jsonl (scripted admit error).
demo-conversation-record: build
    #!/usr/bin/env bash
    set -euo pipefail
    env -u NO_COLOR -u CLICOLOR \
        CLICOLOR_FORCE=1 FORCE_COLOR=1 \
        TERM=xterm-256color COLORTERM=truecolor \
        vhs demos/conversation-record.tape
    just _optimize-demo-gif demos/conversation-record.gif
    ls -lh demos/conversation-record.gif

demo-think-blocks: build
    #!/usr/bin/env bash
    set -euo pipefail
    env -u NO_COLOR -u CLICOLOR \
        CLICOLOR_FORCE=1 FORCE_COLOR=1 \
        TERM=xterm-256color COLORTERM=truecolor \
        vhs demos/think-blocks.tape
    just _optimize-demo-gif demos/think-blocks.gif
    ls -lh demos/think-blocks.gif

demo-launcher-notice: build
    #!/usr/bin/env bash
    set -euo pipefail
    mkdir -p demos/frames
    env -u NO_COLOR -u CLICOLOR \
        CLICOLOR_FORCE=1 FORCE_COLOR=1 \
        TERM=xterm-256color COLORTERM=truecolor \
        vhs demos/launcher-notice.tape
    vhs demos/launcher-notice-short.tape
    just _optimize-demo-gif demos/launcher-notice.gif
    just _optimize-demo-gif demos/launcher-notice-short.gif
    ls -lh demos/launcher-notice.gif demos/launcher-notice-short.gif demos/frames/launcher-notice.png demos/frames/launcher-notice-open.png demos/frames/launcher-notice-short.png demos/frames/launcher-notice-short-scrolled.png

demo-workers: build
    #!/usr/bin/env bash
    set -euo pipefail
    mkdir -p demos/frames
    env -u NO_COLOR -u CLICOLOR \
        CLICOLOR_FORCE=1 FORCE_COLOR=1 \
        TERM=xterm-256color COLORTERM=truecolor \
        vhs demos/workers.tape
    just _optimize-demo-gif demos/workers.gif
    ls -lh demos/workers.gif demos/frames/workers-detail.png

# Conversation: animated background work beside listening, then Ctrl+W detail.
# Scripted bus events and a fixture worker; no mic or model required.
demo-work-cue: build
    #!/usr/bin/env bash
    set -euo pipefail
    mkdir -p demos/frames
    env -u NO_COLOR -u CLICOLOR \
        CLICOLOR_FORCE=1 FORCE_COLOR=1 \
        TERM=xterm-256color COLORTERM=truecolor \
        vhs demos/work-cue.tape
    just _optimize-demo-gif demos/work-cue.gif
    ls -lh demos/work-cue.gif demos/frames/work-cue-*.png

# Settings → Persona: list seeded agents and switch active persona.
demo-persona-switch: build
    #!/usr/bin/env bash
    set -euo pipefail
    env -u NO_COLOR -u CLICOLOR \
        CLICOLOR_FORCE=1 FORCE_COLOR=1 \
        TERM=xterm-256color COLORTERM=truecolor \
        vhs demos/persona-switch.tape
    just _optimize-demo-gif demos/persona-switch.gif
    ls -lh demos/persona-switch.gif

# Duet harness: scripted multi-instance conversation in tmux with full
# artifact capture (tests/duet/README.md). Usage: just duet crosstalk
duet scenario="crosstalk" *flags="": build
    go run ./tests/duet -scenario tests/duet/scenarios/{{scenario}}.yaml {{flags}}

# Persona form: p previews the selected voice; Tab lists prompt variables.
demo-persona-form-ux: build
    #!/usr/bin/env bash
    set -euo pipefail
    env -u NO_COLOR -u CLICOLOR \
        CLICOLOR_FORCE=1 FORCE_COLOR=1 \
        TERM=xterm-256color COLORTERM=truecolor \
        vhs demos/persona-form-ux.tape
    just _optimize-demo-gif demos/persona-form-ux.gif
    ls -lh demos/persona-form-ux.gif

# Personas → Create: boxed form wizard + vim system-prompt editor.
demo-persona-create: build
    #!/usr/bin/env bash
    set -euo pipefail
    env -u NO_COLOR -u CLICOLOR \
        CLICOLOR_FORCE=1 FORCE_COLOR=1 \
        TERM=xterm-256color COLORTERM=truecolor \
        vhs demos/persona-create.tape
    just _optimize-demo-gif demos/persona-create.gif
    ls -lh demos/persona-create.gif

# Opt-in live managed-Qwen gate. Requires the managed model to have already
# been installed with `obey-voice models ensure --tts`; outputs remain available
# for listening instead of disappearing with the Go test temp directory.
qwen-live:
    #!/usr/bin/env bash
    set -euo pipefail
    cache="${XDG_CACHE_HOME:-$HOME/.cache}"
    out="${OBEY_VOICE_QWEN_SMOKE_DIR:-$cache/obey-voice/smoke/qwen}"
    mkdir -p "$out"
    OBEY_VOICE_QWEN_REAL_MODEL=1 OBEY_VOICE_QWEN_SMOKE_DIR="$out" \
        go test -tags=integration -count=1 -timeout 30m -v ./pkg/voiceagent/tts \
        -run '^TestLiveManagedQwenVoiceMatrix$'
    echo "Qwen smoke WAVs: $out"

# Main launcher + Meeting recorder (notes, ★ bookmarks, voice EQ). Full color.
demo-meeting: build
    #!/usr/bin/env bash
    set -euo pipefail
    env -u NO_COLOR -u CLICOLOR \
        CLICOLOR_FORCE=1 FORCE_COLOR=1 \
        TERM=xterm-256color COLORTERM=truecolor \
        vhs demos/meeting.tape
    ls -lh demos/meeting.gif

# Calibre Library browser/viewer (browse → detail → search → audiobook).
# Uses demos/fixtures/fake-calibredb so recording does not need a real library.
demo-library: build
    #!/usr/bin/env bash
    set -euo pipefail
    chmod +x demos/fixtures/fake-calibredb
    env -u NO_COLOR -u CLICOLOR \
        CLICOLOR_FORCE=1 FORCE_COLOR=1 \
        TERM=xterm-256color COLORTERM=truecolor \
        vhs demos/library.tape
    just _optimize-demo-gif demos/library.gif
    ls -lh demos/library.gif

# Meeting route picker + Speaker settings (camp discovery UX). Full color.
# Uses demos/fixtures/camp so recording does not need a real camp registry.
demo-meeting-route-speaker: build
    #!/usr/bin/env bash
    set -euo pipefail
    chmod +x demos/fixtures/camp
    env -u NO_COLOR -u CLICOLOR \
        CLICOLOR_FORCE=1 FORCE_COLOR=1 \
        TERM=xterm-256color COLORTERM=truecolor \
        vhs demos/meeting-route-speaker.tape
    ls -lh demos/meeting-route-speaker.gif

# Multi-voice meeting conversation + speaker analysis status (VHS).
demo-meeting-speakers: build
    #!/usr/bin/env bash
    set -euo pipefail
    env -u NO_COLOR -u CLICOLOR \
        CLICOLOR_FORCE=1 FORCE_COLOR=1 \
        TERM=xterm-256color COLORTERM=truecolor \
        vhs demos/meeting-speakers.tape
    ls -lh demos/meeting-speakers.gif

# Download the multi-speaker YouTube meeting clip (first 90s only, 16 kHz mono).
# Source: https://www.youtube.com/watch?v=lBVtvOpU80Q (~43 min full video — we
# only pull 0:00–1:30). Stored in a shared user cache so worktrees reuse it:
#   ${XDG_CACHE_HOME:-$HOME/.cache}/obey-voice/fixtures/meetings/
# Override with OBEY_VOICE_FIXTURE_CACHE=… or FORCE_FETCH=1 to re-download.
fetch-meeting-fixture:
    #!/usr/bin/env bash
    set -euo pipefail
    chmod +x scripts/fetch-meeting-fixture.sh
    ./scripts/fetch-meeting-fixture.sh

# Download a multi-speaker Zoom clip (source 4:00–5:30, 16 kHz mono).
# Source: https://www.youtube.com/watch?v=DQlWDqosj0E
# Roster: tests/speakerflow/zoom-call-roster.json
fetch-zoom-fixture:
    #!/usr/bin/env bash
    set -euo pipefail
    chmod +x scripts/fetch-zoom-fixture.sh
    ./scripts/fetch-zoom-fixture.sh

# Diarization integration against real multi-voice meeting audio.
#
# 1. Ensures the shared-cache YouTube fixture (auto-fetch once if missing)
# 2. Runs tests/speakerflow
# 3. Refreshes demos/meeting-speakers.gif when `vhs` is installed
#    (skip with SPEAKERFLOW_SKIP_VHS=1)
#
# Also available as: just test speakerflow  (and as part of just test full)
speakerflow:
    #!/usr/bin/env bash
    set -euo pipefail
    # Resolve shared cache path (same rules as scripts/fetch-meeting-fixture.sh).
    if [[ -n "${OBEY_VOICE_MEETING_FIXTURE:-}" ]]; then
        fixture="$OBEY_VOICE_MEETING_FIXTURE"
    else
        # App cache slug matches config.AppSlug / fetch-meeting-fixture.sh —
        # never the retired `obey-voice` slug (WI-afaf92).
        if [[ -n "${OBEY_VOICE_FIXTURE_CACHE:-}" ]]; then
            cache="${OBEY_VOICE_FIXTURE_CACHE}"
        elif [[ -n "${XDG_CACHE_HOME:-}" ]]; then
            cache="${XDG_CACHE_HOME}/obey-voice/fixtures/meetings"
        else
            cache="${HOME}/.cache/obey-voice/fixtures/meetings"
        fi
        fixture="${cache}/product-marketing-meeting-90s.wav"
    fi
    if [[ ! -f "$fixture" ]]; then
        echo "shared fixture missing — fetching once into cache…"
        just fetch-meeting-fixture
    fi
    if [[ ! -f "$fixture" ]]; then
        echo "fixture still missing after fetch: $fixture" >&2
        exit 1
    fi
    echo "==> speakerflow: using $fixture"
    echo "==> speakerflow: native fixture tests"
    go test -tags=speakerflow -count=1 -timeout 3m -v ./tests/speakerflow/...
    if [[ "${SPEAKERFLOW_SKIP_VHS:-}" == "1" ]]; then
        echo "==> speakerflow: skipping VHS (SPEAKERFLOW_SKIP_VHS=1)"
        exit 0
    fi
    if ! command -v vhs >/dev/null 2>&1; then
        echo "==> speakerflow: vhs not installed — skip meeting-speakers.gif refresh"
        echo "    install vhs to auto-update demos/meeting-speakers.gif"
        exit 0
    fi
    echo "==> speakerflow: refreshing demos/meeting-speakers.gif"
    just demo-meeting-speakers

[private]
_optimize-demo-gif path:
    #!/usr/bin/env bash
    set -euo pipefail
    gif="{{path}}"
    palette="$(mktemp /tmp/obey-voice-demo-palette.XXXXXX.png)"
    optimized="$(mktemp /tmp/obey-voice-demo-output.XXXXXX.gif)"
    trap 'rm -f "$palette" "$optimized"' EXIT
    # Optional compact pass for README weight — prefer full palette.
    ffmpeg -y -loglevel error -i "$gif" \
        -vf "fps=20,scale=1000:-1:flags=lanczos,palettegen=max_colors=256:stats_mode=full" \
        "$palette"
    ffmpeg -y -loglevel error -i "$gif" -i "$palette" \
        -lavfi "fps=20,scale=1000:-1:flags=lanczos,paletteuse=dither=bayer:bayer_scale=3:diff_mode=rectangle" \
        "$optimized"
    mv "$optimized" "$gif"
    trap - EXIT
    rm -f "$palette"
    ls -lh "$gif"

# Build, sign, install to $GOBIN, then download the default STT/TTS/VAD stack.
# Qwen is opt-in and only downloads on platforms with a published native package.
# Set OBEY_VOICE_SKIP_MODELS_ENSURE=1 for a binary-only install.
install: build
    #!/usr/bin/env bash
    set -euo pipefail
    mkdir -p {{gobin}}
    cp {{bin_dir}}/{{binary_name}} {{gobin}}/{{binary_name}}
    if [[ "$(uname)" == "Darwin" ]]; then
        codesign --force --sign - {{gobin}}/{{binary_name}} 2>/dev/null || \
        echo "Warning: Could not sign binary (non-fatal)"
    fi
    # One-cycle compatibility alias for scripts and muscle memory predating the rename.
    ln -sfn {{gobin}}/{{binary_name}} {{gobin}}/samantha
    echo "obey-voice installed to {{gobin}}/{{binary_name}} (alias: {{gobin}}/samantha)"
    if [[ "${OBEY_VOICE_SKIP_MODELS_ENSURE:-}" == "1" ]]; then
        echo "Skipping model download (OBEY_VOICE_SKIP_MODELS_ENSURE=1). Run: {{binary_name}} models ensure"
    else
        echo "Downloading default model assets (STT/TTS/VAD for this config)..."
        if ! "{{gobin}}/{{binary_name}}" models ensure; then
            echo "Binary installed; voice setup is incomplete. Resolve the model/runtime error above, then run: {{binary_name}} models ensure"
        fi
    fi

# Build and run with any args
run *ARGS: build
    ./{{bin_dir}}/{{binary_name}} {{ARGS}}

# Talk to Obey Voice (full voice mode)
talk: build
    ./{{bin_dir}}/{{binary_name}}

# Full pipeline: clean, build, then the complete test suite
# (unit + Docker integration + voiceflow + speakerflow/VHS + audio-crackle).
all:
    just clean
    just build
    just test full

# Clean build artifacts
clean:
    @{{BUILDTOOL}} clean
