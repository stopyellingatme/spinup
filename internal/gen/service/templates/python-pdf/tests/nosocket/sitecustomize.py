"""Test helper: every socket of this process fails.

Python imports this module at start when this directory is on PYTHONPATH.
Only tests/test_render.py does that, for a render child. Production code has
no hook for it.
"""

import socket


class NoSocket(socket.socket):
    """A socket class that refuses to make a socket."""

    def __init__(self, *args, **kwargs):
        raise OSError("socket blocked by tests/nosocket/sitecustomize.py")


socket.socket = NoSocket
