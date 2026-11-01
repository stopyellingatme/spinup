"""The render child: python -m service.render_job.

The child reads {"template": ..., "data": ...} as JSON from standard input and
writes the PDF to standard output. It sets RLIMIT_AS before it imports
WeasyPrint. On a failure it writes one line with the error class to standard
error and exits with a code from service.limits. It never writes the data.
"""

import json
import logging
import resource
import sys

from service import limits


def main() -> int:
    """Render one request and return the exit code."""
    settings = limits.Limits.from_env()
    resource.setrlimit(resource.RLIMIT_AS, (settings.max_memory_bytes, settings.max_memory_bytes))
    logging.disable(logging.CRITICAL)

    try:
        request = json.loads(sys.stdin.buffer.read())
        template, data = request["template"], request["data"]
        if not isinstance(template, str) or not isinstance(data, dict):
            raise TypeError("template must be a string and data an object")
    except (ValueError, KeyError, TypeError) as error:
        return fail(limits.EXIT_BAD_REQUEST, error)

    try:
        from jinja2 import TemplateNotFound, UndefinedError
        from jinja2.exceptions import SecurityError
        from weasyprint.urls import FatalURLFetchingError

        from service.render import UnknownTemplate, render_pdf
    except MemoryError as error:
        return fail(limits.EXIT_FAILED, error)

    try:
        pdf = render_pdf(template, data)
    except (UnknownTemplate, TemplateNotFound) as error:
        return fail(limits.EXIT_NOT_FOUND, error)
    except UndefinedError as error:
        # The message names the missing key and never holds a data value.
        return fail(limits.EXIT_DATA, error, str(error))
    except FatalURLFetchingError as error:
        return fail(limits.EXIT_FETCH, error)
    except SecurityError as error:
        return fail(limits.EXIT_FAILED, error)
    except BaseException as error:  # noqa: BLE001 - the child reports every failure as one line
        return fail(limits.EXIT_FAILED, error)

    sys.stdout.buffer.write(pdf)
    sys.stdout.buffer.flush()
    return limits.EXIT_OK


def fail(code: int, error: BaseException, detail: str = "") -> int:
    """Write the error class, and an optional detail, to standard error."""
    line = type(error).__name__
    if detail:
        line += ": " + detail.replace("\n", " ")[:500]
    sys.stderr.write(line + "\n")
    sys.stderr.flush()
    return code


if __name__ == "__main__":
    sys.exit(main())
