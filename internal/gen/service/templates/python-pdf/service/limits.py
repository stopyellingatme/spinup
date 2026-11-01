"""Paths and limits of the PDF service.

This module imports no third-party package. The render child reads it before
it sets its memory limit.
"""

import os
import re
from dataclasses import dataclass
from pathlib import Path

PACKAGE_DIR = Path(__file__).resolve().parent
APP_DIR = PACKAGE_DIR.parent
TEMPLATES_DIR = PACKAGE_DIR / "templates"
ASSETS_DIR = PACKAGE_DIR / "assets"

TEMPLATE_PATTERN = re.compile(r"[a-z][a-z0-9_]{0,63}")
TEMPLATE_SUFFIX = ".html.j2"

MAX_OUTPUT_BYTES = 20 * 1024 * 1024
MAX_STDERR_BYTES = 64 * 1024
RETRY_AFTER_SECONDS = 5

# Exit codes of the render child.
EXIT_OK = 0
EXIT_FAILED = 1
EXIT_BAD_REQUEST = 2
EXIT_NOT_FOUND = 3
EXIT_DATA = 4
EXIT_FETCH = 5


@dataclass(frozen=True)
class Limits:
    """Limits of the render path. The environment overrides each default."""

    max_body_bytes: int = 1024 * 1024
    timeout_seconds: float = 20.0
    max_memory_bytes: int = 2 * 1024 * 1024 * 1024
    concurrency: int = 2
    queue_max: int = 4

    @classmethod
    def from_env(cls, env: dict[str, str] | None = None) -> "Limits":
        """Read the limits from RENDER_* environment variables."""
        env = os.environ if env is None else env
        defaults = cls()
        return cls(
            max_body_bytes=_read(env, "RENDER_MAX_BODY_BYTES", defaults.max_body_bytes, int, 1),
            timeout_seconds=_read(env, "RENDER_TIMEOUT_SECONDS", defaults.timeout_seconds, float, 0.1),
            max_memory_bytes=_read(env, "RENDER_MAX_MEMORY_BYTES", defaults.max_memory_bytes, int, 64 * 1024 * 1024),
            concurrency=_read(env, "RENDER_CONCURRENCY", defaults.concurrency, int, 1),
            queue_max=_read(env, "RENDER_QUEUE_MAX", defaults.queue_max, int, 0),
        )


def _read(env, name, default, kind, minimum):
    raw = env.get(name, "")
    if raw == "":
        return default
    try:
        value = kind(raw)
    except ValueError:
        raise ValueError(f"{name}={raw!r} is not a number. Try: unset {name} or set a number") from None
    if value < minimum:
        raise ValueError(f"{name}={raw!r} is below {minimum}. Try: set {name} to {minimum} or more")
    return value


def template_path(name: str) -> Path | None:
    """Return the file of a template name, or None when the name is not valid."""
    if not isinstance(name, str) or not TEMPLATE_PATTERN.fullmatch(name):
        return None
    return TEMPLATES_DIR / (name + TEMPLATE_SUFFIX)
