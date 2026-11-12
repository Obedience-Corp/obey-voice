#!/bin/sh
# Stands in for `models move`: counts SIGTERMs into the file named by its
# last argument and finishes on its own after about seven seconds.
log=$4
trap 'echo term >> "$log"' TERM
echo ready
i=0
while [ "$i" -lt 70 ]; do
  sleep 0.1
  i=$((i + 1))
done
echo '{"type":"result"}'
