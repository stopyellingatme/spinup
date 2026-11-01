"""The URL fetcher of WeasyPrint. It allows only data URLs and local assets."""

import os
from pathlib import Path
from urllib.parse import urlsplit
from urllib.request import url2pathname

from weasyprint.urls import FatalURLFetchingError, URLFetcher

from service.limits import ASSETS_DIR


class AssetFetcher(URLFetcher):
    """Fetch data URLs and files below the assets directory, and nothing else.

    A refused URL raises FatalURLFetchingError, so the render fails. WeasyPrint
    swallows any other exception type and skips the resource without an error.
    """

    def __init__(self, assets_dir: Path = ASSETS_DIR) -> None:
        super().__init__(fail_on_errors=True)
        self._assets = os.path.realpath(assets_dir)

    def fetch(self, url, headers=None):
        """Fetch an allowed URL. Raise FatalURLFetchingError for any other URL."""
        try:
            parts = urlsplit(url)
            scheme = parts.scheme.lower()
            if scheme == "data":
                return super().fetch(url, headers)
            if scheme == "file" and parts.netloc in ("", "localhost"):
                real = os.path.realpath(url2pathname(parts.path))
                if os.path.commonpath([real, self._assets]) == self._assets and os.path.isfile(real):
                    # Fetch the checked path, not the URL as written.
                    return super().fetch(Path(real).as_uri(), headers)
        except FatalURLFetchingError:
            raise
        except Exception:
            pass
        raise FatalURLFetchingError(f"blocked URL with the scheme {_scheme(url)!r}")


def _scheme(url) -> str:
    try:
        return urlsplit(url).scheme.lower()[:16]
    except Exception:
        return "?"
