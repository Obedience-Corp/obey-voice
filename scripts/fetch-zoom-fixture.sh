#!/usr/bin/env bash
# Download a short multi-speaker Zoom-style call clip for speaker-id accuracy tests.
#
# Source: Example Zoom Meeting (student tutorial, multi-speaker English)
#   https://www.youtube.com/watch?v=DvO10dFJaFE
#
# Only the first ~2 minutes are downloaded. The WAV is stored in the shared
# user cache (same scheme as scripts/fetch-meeting-fixture.sh):
#
#   ${OBEY_VOICE_FIXTURE_CACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/festival-voice/fixtures/meetings}
#
# YouTube Terms of Service apply; the clip is not committed to git.
# Override URL/section with ZOOM_FIXTURE_URL / ZOOM_FIXTURE_SECTION.
set -euo pipefail

APP_SLUG="${APP_SLUG:-festival-voice}"

STEM="zoom-call"
URL="${ZOOM_FIXTURE_URL:-https://www.youtube.com/watch?v=DvO10dFJaFE}"
SECTION="${ZOOM_FIXTURE_SECTION:-*0:00-2:00}"

if [[ -n "${OBEY_VOICE_FIXTURE_CACHE:-}" ]]; then
	CACHE_DIR="${OBEY_VOICE_FIXTURE_CACHE}"
elif [[ -n "${XDG_CACHE_HOME:-}" ]]; then
	CACHE_DIR="${XDG_CACHE_HOME}/${APP_SLUG}/fixtures/meetings"
else
	CACHE_DIR="${HOME}/.cache/${APP_SLUG}/fixtures/meetings"
fi

OUT_WAV="${CACHE_DIR}/${STEM}.wav"
mkdir -p "$CACHE_DIR"

if [[ -f "$OUT_WAV" && "${FORCE_FETCH:-}" != "1" ]]; then
	sz="$(wc -c <"$OUT_WAV" | tr -d ' ')"
	if [[ "$sz" -gt 100000 ]]; then
		echo "zoom fixture already cached (shared): $OUT_WAV"
		ls -lh "$OUT_WAV"
		echo "hint: FORCE_FETCH=1 just fetch-zoom-fixture  # re-download"
		exit 0
	fi
	echo "cached file looks truncated (${sz} bytes) — re-fetching…"
fi

command -v yt-dlp >/dev/null || { echo "yt-dlp required" >&2; exit 1; }
command -v ffmpeg >/dev/null || { echo "ffmpeg required" >&2; exit 1; }

tmp="$(mktemp -d "${TMPDIR:-/tmp}/${APP_SLUG}-zoom-fixture.XXXXXX")"
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT

echo "Fetching ${URL} section ${SECTION} → 16 kHz mono WAV (shared cache)…"
echo "cache: $OUT_WAV"
yt-dlp -f 'bestaudio/best' \
	--download-sections "$SECTION" \
	-x --audio-format wav \
	--postprocessor-args "ffmpeg:-ac 1 -ar 16000" \
	-o "${tmp}/${STEM}.%(ext)s" \
	"$URL"

found="$(find "$tmp" -type f \( -name '*.wav' -o -name '*.wave' \) | head -1)"
if [[ -z "$found" ]]; then
	echo "download produced no wav in $tmp:" >&2
	ls -la "$tmp" >&2
	exit 1
fi

tmp_out="${OUT_WAV}.tmp.$$"
ffmpeg -y -loglevel error -i "$found" -ac 1 -ar 16000 "$tmp_out"
mv -f "$tmp_out" "$OUT_WAV"
ls -lh "$OUT_WAV"
file "$OUT_WAV"
echo "Wrote shared zoom fixture: $OUT_WAV"
echo "Source: $URL (section ${SECTION} only)"
echo "Roster: tests/speakerflow/zoom-call-roster.json"
echo "All worktrees reuse this path; set OBEY_VOICE_FIXTURE_CACHE to override."
