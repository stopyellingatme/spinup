"""The client bundle size of a built SvelteKit app (ADR 0034 sections 5, 6, and 8).

Python 3 standard library only. Run it as `python3 -I scripts/perf/bundle.py`.

Usage:
  bundle.py measure <build-dir>                 print the bundle bytes as JSON
  bundle.py check <build-dir> <budgets.json>    exit 1 when the bundle is above a budget

<build-dir> is `web/build` of a node build (the client files are in `client/`) or of an
embed build (the client files are at the top). The script counts the `.js` and `.css` files
under `_app/immutable/`. The `.br` and `.gz` siblings are not in `raw_bytes`. They are summed
apart in `br_bytes` and `gz_bytes`. When a file has no sibling, the browser gets the plain
file, so the plain size counts in that sum.

The budgets file is a JSON object. The keys `web_bundle_raw_bytes` and `web_bundle_br_bytes`
are the limits. A missing key means no limit.

Exit codes: 0 in budget (or measured), 1 above a budget, 2 for a usage or input error.
"""

import html.parser
import json
import sys
import urllib.parse
from pathlib import Path

ASSET_SUFFIXES = (".js", ".css")
IMMUTABLE = ("_app", "immutable")
LARGEST = 5
BUDGET_KEYS = (("raw_bytes", "web_bundle_raw_bytes"), ("br_bytes", "web_bundle_br_bytes"))


class BundleError(Exception):
    """A bad input. The message says what failed and why. The hint is the Try: line."""

    def __init__(self, message, hint):
        super().__init__(message)
        self.hint = hint


def client_root(build_dir):
    """The directory that the browser sees as `/`: `client/` of a node build, else build_dir."""
    build_dir = Path(build_dir)
    if not build_dir.is_dir():
        raise BundleError(f"{build_dir} is not a directory, so there is no build to measure",
                          "build the web app first (pnpm build), then pass its build directory")
    client = build_dir / "client"
    if (client / Path(*IMMUTABLE)).is_dir():
        return client
    if (build_dir / Path(*IMMUTABLE)).is_dir():
        return build_dir
    raise BundleError(f"{build_dir} has no {'/'.join(IMMUTABLE)} directory (also not under client/)",
                      "pass web/build of a finished SvelteKit build")


def _sibling_size(path, suffix):
    sib = path.with_name(path.name + suffix)
    return sib.stat().st_size if sib.is_file() else path.stat().st_size


def file_sizes(path):
    """{"raw_bytes", "br_bytes", "gz_bytes"} of one file. A missing sibling counts the plain size."""
    path = Path(path)
    return {"raw_bytes": path.stat().st_size, "br_bytes": _sibling_size(path, ".br"),
            "gz_bytes": _sibling_size(path, ".gz")}


def measure(build_dir):
    """The bundle bytes of build_dir. Returns a dict that `measure` prints as JSON."""
    root = client_root(build_dir)
    immutable = root / Path(*IMMUTABLE)
    files = []
    for p in sorted(immutable.rglob("*")):
        if p.is_file() and not p.is_symlink() and p.suffix in ASSET_SUFFIXES:
            files.append({"file": p.relative_to(root).as_posix(), **file_sizes(p)})
    total = {k: sum(f[k] for f in files) for k in ("raw_bytes", "br_bytes", "gz_bytes")}
    largest = sorted(files, key=lambda f: (-f["raw_bytes"], f["file"]))[:LARGEST]
    return {"files": len(files), **total, "largest": largest}


def read_budgets(path):
    """The budget limits of a budgets file: {key: int}. A missing key is not in the dict."""
    try:
        data = json.loads(Path(path).read_text())
    except OSError as e:
        raise BundleError(f"cannot read the budgets file {path}: {e.strerror}",
                          "pass perf/budgets.json of the repository") from e
    except ValueError as e:
        raise BundleError(f"the budgets file {path} is not JSON: {e}", "fix the JSON syntax of the file") from e
    if not isinstance(data, dict):
        raise BundleError(f"the budgets file {path} is not a JSON object",
                          'write an object such as {"web_bundle_br_bytes": 400000}')
    limits = {}
    for _, key in BUDGET_KEYS:
        if key not in data:
            continue
        v = data[key]
        if isinstance(v, bool) or not isinstance(v, int) or v < 0:
            raise BundleError(f"{key} in {path} is {v!r}, and it must be a whole number of bytes, 0 or more",
                              f'write "{key}": <bytes>, or remove the key for no limit')
        limits[key] = v
    return limits


def check(result, limits):
    """Compares a measure() result with the limits. Returns (ok, message)."""
    over = []
    for field, key in BUDGET_KEYS:
        if key in limits and result[field] > limits[key]:
            over.append(f"{field} {result[field]:,} is above {key} {limits[key]:,}")
    if over:
        big = ", ".join(f"{f['file']} ({f['raw_bytes']:,} raw, {f['br_bytes']:,} br)" for f in result["largest"])
        return False, ("bundle.py: the client bundle is above the budget: " + "; ".join(over)
                       + f"\nLargest files: {big}"
                       + "\nTry: make the client code smaller, or raise the budget in the budgets file "
                         "with the reason in the commit message")
    parts = [f"{field} {result[field]:,}" + (f" (limit {limits[key]:,})" if key in limits else " (no limit)")
             for field, key in BUDGET_KEYS]
    return True, f"bundle.py: client bundle in budget: {', '.join(parts)}, gz_bytes {result['gz_bytes']:,}"


# ---------------------------------------------------------------------------
# The first load of one page
# ---------------------------------------------------------------------------


class _Targets(html.parser.HTMLParser):
    """Collects the modulepreload links, the script sources, and the import() calls of inline scripts."""

    def __init__(self):
        super().__init__(convert_charrefs=True)
        self.urls = []
        self._inline = False

    def handle_starttag(self, tag, attrs):
        a = {k.lower(): (v or "") for k, v in attrs}
        if tag == "link" and "modulepreload" in a.get("rel", "").lower().split() and a.get("href"):
            self.urls.append(a["href"])
        elif tag == "script":
            if a.get("src"):
                self.urls.append(a["src"])
            else:
                self._inline = True

    def handle_endtag(self, tag):
        if tag == "script":
            self._inline = False

    def handle_data(self, data):
        if not self._inline:
            return
        i = 0
        while True:
            i = data.find("import(", i)
            if i < 0:
                return
            j = i + len("import(")
            q = data[j:j + 1]
            if q in ("'", '"', "`"):
                end = data.find(q, j + 1)
                if end > 0:
                    self.urls.append(data[j + 1:end])
            i = j


def page_targets(page_html, page_path="/"):
    """The paths (such as /_app/immutable/entry/start.X.js) that the page loads as scripts, in order."""
    p = _Targets()
    p.feed(page_html)
    p.close()
    base = urllib.parse.urljoin("http://page.invalid/", page_path)
    out = []
    for u in p.urls:
        full = urllib.parse.urlsplit(urllib.parse.urljoin(base, u))
        if full.netloc != "page.invalid":
            continue  # another origin: not a file of this build
        if full.path not in out:
            out.append(full.path)
    return out


def first_load_br_bytes(page_html, page_path, build_dir):
    """The brotli bytes of the script targets of a page (the plain size when a file has no .br).

    Returns (bytes, number of files). Raises BundleError when a target is not in the build.
    """
    root = client_root(build_dir)
    total = 0
    paths = page_targets(page_html, page_path)
    for path in paths:
        f = root / urllib.parse.unquote(path).lstrip("/")
        if not f.is_file():
            raise BundleError(f"the page {page_path} loads {path}, and the build has no {f}",
                              "build the web app again, and start the server from the same build")
        total += _sibling_size(f, ".br")
    return total, len(paths)


# ---------------------------------------------------------------------------
# Command line
# ---------------------------------------------------------------------------

USAGE = ("usage: bundle.py measure <build-dir>\n"
         "       bundle.py check <build-dir> <budgets.json>")


def main(argv):
    if len(argv) >= 1 and argv[0] in ("-h", "--help"):
        print(USAGE)
        return 0
    if not argv or (argv[0], len(argv)) not in (("measure", 2), ("check", 3)):
        print(f"{USAGE}\nTry: bundle.py measure web/build", file=sys.stderr)
        return 2
    try:
        result = measure(argv[1])
        if argv[0] == "measure":
            print(json.dumps(result, indent=2))
            return 0
        ok, message = check(result, read_budgets(argv[2]))
    except BundleError as e:
        print(f"bundle.py: {e}\nTry: {e.hint}", file=sys.stderr)
        return 2
    print(message, file=sys.stdout if ok else sys.stderr)
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
