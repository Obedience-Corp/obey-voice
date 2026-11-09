"""Keep a private decision worker alive only while its Go owner is alive."""
import os
import selectors
import signal
import subprocess
import sys

LISTENER_FD = 3
OWNER_FD = 4


def stop_owned_group():
    # Go starts this supervisor in its own group. Kill the whole group, including
    # this watcher and multiprocessing children, even if inference is wedged.
    os.killpg(os.getpgrp(), signal.SIGKILL)


if os.getpgrp() != os.getpid():
    raise SystemExit("decision supervisor requires its own process group")

with selectors.DefaultSelector() as watcher:
    watcher.register(OWNER_FD, selectors.EVENT_READ)
    # The owner may have crashed before this interpreter even started. Do not
    # load model weights when its lifetime pipe has already closed.
    if watcher.select(0):
        stop_owned_group()
    try:
        worker = subprocess.Popen(
            [sys.executable, *sys.argv[1:]], pass_fds=(LISTENER_FD,)
        )
        os.close(LISTENER_FD)
        # A separate process watches the pipe: inference cannot prevent this
        # cleanup by blocking Python's GIL. The worker never inherits OWNER_FD.
        while worker.poll() is None:
            if watcher.select(0.1):
                stop_owned_group()
    finally:
        # A worker that exits on its own must not leave its descendants behind.
        stop_owned_group()
