#!/bin/sh
# Deterministic speech download process; never reads config or downloads files.
printf '%s\n' '{"asset":"tdt-0.6b-v3","phase":"download","pct":25}'
if [ "${1:-}" = "wait" ]; then
    trap 'printf '\''%s\n'\'' '\''{"done":true,"error":"Download canceled"}'\''; exit 1' TERM
    while :; do sleep 1; done
fi
printf '%s\n' '{"done":true,"installed":["tdt-0.6b-v3"]}'
