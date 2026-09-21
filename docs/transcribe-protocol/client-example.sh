#!/bin/sh
# client-example.sh — transcribe a WAV through obey-transcript/v1.
#
# An independent client: POSIX shell and jq, no obey-voice library, no Go. It
# is the worked example for the rules in docs/transcribe-protocol.md.
#
#   ./client-example.sh /absolute/path/audio.wav
#
# The WAV must already be 16 kHz mono PCM s16le. OBEY_VOICE_BIN names the
# binary when it is not on PATH.
set -eu

BIN=${OBEY_VOICE_BIN:-obey-voice}
PROTOCOL="obey-transcript/v1"
MAX_LINE=1048576

if [ $# -ne 1 ]; then
	echo "usage: $0 /absolute/path/audio.wav" >&2
	exit 64
fi
audio=$1
case $audio in
/*) ;;
*)
	echo "client: input path must be absolute: $audio" >&2
	exit 64
	;;
esac
command -v jq >/dev/null 2>&1 || {
	echo "client: jq is required" >&2
	exit 69
}

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM

job_id="client-example-$$"
# The request is built with jq so the path is JSON-escaped, never pasted.
jq -n --arg protocol "$PROTOCOL" --arg job "$job_id" --arg path "$audio" '{
  protocol: $protocol,
  job_id: $job,
  input: {
    path: $path,
    format: "wav_pcm_s16le",
    sample_rate_hz: 16000,
    channels: 1,
    timeline_origin_us: 0
  },
  provider: "configured",
  timing: "utterance",
  diarization: false,
  allow_model_download: false
}' >"$work/request.json"

# Both pipes are drained into files, so neither can fill and block the child.
# Redirecting stdout and stderr to files is the simplest correct way to drain
# them concurrently: a child blocked writing to a full pipe never exits, and a
# file never fills a pipe buffer. A client in another language uses two reader
# threads instead.
# Never build this command through a shell string: the path is an argument.
set +e
"$BIN" transcribe --request "$work/request.json" --jsonl >"$work/events.jsonl" 2>"$work/diagnostics.txt"
status=$?
set -e

# Bound the line size in bytes before parsing anything. A client that hands an
# unbounded line to a JSON parser has already lost the protection the cap exists
# to give. LC_ALL=C makes awk length() count bytes, not locale characters.
if LC_ALL=C awk -v max="$MAX_LINE" 'length($0) + 1 > max { exit 1 }' "$work/events.jsonl"; then
	:
else
	echo "client: an event line is over the ${MAX_LINE} byte cap" >&2
	exit 65
fi

# Rules: the protocol string, the job ID, seq consecutive from 0, sane
# intervals, and exactly one terminal event.
expect_seq=0
terminal=""
segments=0
last_start=-1
while IFS= read -r line; do
	[ -n "$line" ] || continue
	if [ -n "$terminal" ]; then
		echo "client: an event followed the terminal $terminal event" >&2
		exit 65
	fi
	if ! printf '%s\n' "$line" | jq -e \
		--arg protocol "$PROTOCOL" --arg job "$job_id" --argjson seq "$expect_seq" \
		'.protocol == $protocol and .job_id == $job and .seq == $seq' >/dev/null; then
		echo "client: event $expect_seq has the wrong protocol, job_id or seq: $line" >&2
		exit 65
	fi
	type=$(printf '%s\n' "$line" | jq -r '.type')
	case $type in
	segment)
		start=$(printf '%s\n' "$line" | jq -r '.start_us')
		end=$(printf '%s\n' "$line" | jq -r '.end_us')
		if [ "$start" -lt 0 ] || [ "$end" -le "$start" ] || [ "$start" -lt "$last_start" ]; then
			echo "client: segment interval ${start}..${end} us is not usable" >&2
			exit 65
		fi
		last_start=$start
		segments=$((segments + 1))
		;;
	completed | failed | cancelled)
		terminal=$type
		;;
	esac
	expect_seq=$((expect_seq + 1))
done <"$work/events.jsonl"

# Rule: a stream with no terminal event is a failure, whatever the exit status.
if [ -z "$terminal" ]; then
	echo "client: the stream ended with no terminal event (exit $status)" >&2
	sed -n '1,20p' "$work/diagnostics.txt" >&2
	exit 65
fi

# Rule: success needs both the completed event and exit 0.
if [ "$terminal" != completed ] || [ "$status" -ne 0 ]; then
	failure=$(jq -r 'select(.type == "failed") | "client: \(.code): \(.message) (retryable: \(.retryable))"' "$work/events.jsonl")
	[ -z "$failure" ] || echo "$failure" >&2
	echo "client: terminal event $terminal, exit $status" >&2
	sed -n '1,20p' "$work/diagnostics.txt" >&2
	# A failed or cancelled terminal with child exit 0 is still a client
	# failure: success requires completed plus exit 0.
	if [ "$status" -ne 0 ]; then
		exit "$status"
	fi
	exit 65
fi

outcome=$(jq -r 'select(.type == "completed") | .outcome' "$work/events.jsonl")
if [ "$outcome" = no_speech ]; then
	echo "(no speech in $audio)"
	exit 0
fi

jq -r 'select(.type == "segment") |
  (.start_us / 1000 | floor) as $ms |
  "[\(($ms / 60000) | floor):\((($ms % 60000) / 1000) | floor | tostring | if length < 2 then "0" + . else . end)] \(.text)"' \
	"$work/events.jsonl"
echo "($segments segment(s) from $audio)" >&2
