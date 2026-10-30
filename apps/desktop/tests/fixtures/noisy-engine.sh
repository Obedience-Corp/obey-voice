#!/bin/sh
printf '%s\n' '{"event":"ready"}' 'native log noise' '{"event":"state","data":{"mic":true}}'
read request
printf '%s\n' '{"event":"saved"}'
