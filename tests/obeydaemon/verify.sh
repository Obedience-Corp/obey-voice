#!/usr/bin/env bash
set -euo pipefail
[ -f /.dockerenv ] && [ "$(id -u)" != 0 ] || { echo 'Disposable non-root Docker fixture required' >&2; exit 2; }
mkdir -p "$HOME/bin" "$HOME/evidence"
trap 'cat "$HOME/evidence/bootstrap.log" "$HOME/evidence/install.log" "$HOME/evidence/failure-paths.log" "$HOME/evidence/real-daemon.log" 2>/dev/null || true' EXIT
curl --fail --silent --show-error --location --retry 3 --max-time 120 \
 https://raw.githubusercontent.com/Obedience-Corp/festival/main/install.sh -o "$HOME/festival-install.sh"
# Keep exercising the public installer, but tolerate transient release-CDN
# failures. The outer container timeout still bounds the entire acceptance run.
for attempt in 1 2 3; do
 if VERSION="$FESTIVAL_VERSION" INSTALL_DIR="$HOME/bin" bash "$HOME/festival-install.sh" --no-shell >> "$HOME/evidence/bootstrap.log" 2>&1; then
  break
 fi
 if [ "$attempt" = 3 ]; then exit 1; fi
 sleep 2
done
for attempt in 1 2 3; do
 if festival install obey --channel stable --no-shell-setup --no-restart --json > "$HOME/evidence/install.json" 2>> "$HOME/evidence/install.log"; then
  break
 fi
 if [ "$attempt" = 3 ]; then exit 1; fi
 sleep 2
done
obey version > "$HOME/evidence/obey-version.txt"
if ! grep -Fxq "obey $OBEY_EXPECT_VERSION" "$HOME/evidence/obey-version.txt"; then
 echo "Expected Obey $OBEY_EXPECT_VERSION; installed executable reported:" >&2
 cat "$HOME/evidence/obey-version.txt" >&2
 exit 1
fi
festival list --json > "$HOME/evidence/packages.json"
go list -m github.com/Obedience-Corp/obey-go-sdk > "$HOME/evidence/sdk-module.txt"
VOICE_CATALOG_CONTAINER=1 go test -tags=integration -race -count=1 -timeout=2m -run TestObeyCatalogIntegration -v ./internal/discovery > "$HOME/evidence/failure-paths.log" 2>&1
VOICE_REAL_OBEY_CONTAINER=1 go test -tags=integration -race -count=1 -timeout=2m -v ./tests/obeydaemon > "$HOME/evidence/real-daemon.log" 2>&1
