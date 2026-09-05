#!/usr/bin/env python3
"""Run a command with a timeout. Prints TIMED_OUT on stderr if the deadline fires."""
import subprocess
import sys

def main() -> int:
    if len(sys.argv) < 3:
        print("usage: watchdog.py SECONDS CMD...", file=sys.stderr)
        return 2
    sec = int(sys.argv[1])
    try:
        return subprocess.run(sys.argv[2:], timeout=sec).returncode
    except subprocess.TimeoutExpired:
        print("TIMED_OUT", file=sys.stderr)
        return 1

if __name__ == "__main__":
    raise SystemExit(main())
