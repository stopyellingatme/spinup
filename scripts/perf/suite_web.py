"""The web suite: the SvelteKit app in node mode and the embed binary (ADR 0034 sections 2 and 6).

The scenario app: `spinup new` with the modules ci and auth (node mode), then
`g resource note title:string body:text?` with its web pages. The suite installs the web
packages from the offline pnpm store, builds the API (as the Dockerfile builds it) and the
web app, starts the API and the Node server (`node --import <instrumentation> build`, the
start command of Dockerfile.web), signs in, and seeds notes.

Phases:
  1. Conformance (no timing): the signed-in list page answers 200 with a seeded title and a
     CSP nonce, an immutable asset answers with `content-encoding: br`, and a second request
     with `If-None-Match` answers 304. A failure stops the suite and records nothing.
  2. Node mode: the build time, the bundle bytes (scripts/perf/bundle.py), the first-load
     bytes of the sign-in page, start to /healthz, the RSS, the SSR load on the list page and
     on the sign-in page, the time to first byte, and the API requests for each page.
  3. Embed mode (skipped with --quick): `spinup build` of an embed app, the binary size, the
     same conformance checks against the binary, and the load on the index page.
"""

import http.client
import json
import os
import shutil
import signal
import subprocess
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import bundle  # noqa: E402
import lib  # noqa: E402
import suite_app  # noqa: E402

SUITE = "web"
MODULES = "ci,auth"
EMBED_MODULES = "ci"
RESOURCE = ["note", "title:string", "body:text?"]
PAGE_PATH = "/notes"
LOGIN_PATH = "/login"
SEED_TITLE = "perf note"
SEED_ROWS = 100
CONCURRENCY = 32
COUNT_REQUESTS = 100
COUNT_CONCURRENCY = 4
IDLE_WAIT_S = 2.0
HEALTH_POLL_S = 0.005
ACCESS_LOG_MARK = "HTTP request"
INSTRUMENTATION = Path("build") / "server" / "instrumentation.server.js"

LATENCY_INFO = "the same load run as the req/s metric; latency percentiles are noisy on a shared machine"
TTFB_INFO = "one connection, sequential requests; time to first byte is noisy on a shared machine"


# ---------------------------------------------------------------------------
# Small helpers
# ---------------------------------------------------------------------------


def web_env(ctx, **extra):
    """The run env with scripts/bin (pnpm through corepack) first on PATH."""
    env = ctx.cmd_env(**extra)
    env["PATH"] = str(ctx.repo / "scripts" / "bin") + os.pathsep + env.get("PATH", "")
    return env


def node_binary(ctx):
    """The path of `node` on the PATH of the run, and its version."""
    env = web_env(ctx)
    node = shutil.which("node", path=env["PATH"])
    if not node:
        raise lib.Failure("node is not on PATH, and the web suite needs Node 24 to build and run the web app",
                          "run the suite with make perf (it puts Node 24 on PATH), or add Node 24 to PATH")
    _, out, _ = lib.run([node, "--version"], env=env, timeout=60, capture=True)
    return node, out.strip()


def http_get(port, path, headers=None):
    """One GET request. Returns (status, headers with lower-case names, body bytes)."""
    c = http.client.HTTPConnection("127.0.0.1", port, timeout=30)
    try:
        c.request("GET", path, headers=headers or {})
        r = c.getresponse()
        return r.status, {k.lower(): v for k, v in r.getheaders()}, r.read()
    finally:
        c.close()


def loadgen(ctx, url, concurrency=CONCURRENCY, cookie=None, expect_body=None, headers=(), requests=None,
            warmup="1s"):
    """Runs scripts/perf/loadgen against url. Returns its JSON. Failure on any failed request."""
    argv = [ctx.loadgen, "-url", url, "-c", str(concurrency)]
    argv += ["-n", str(requests)] if requests else ["-d", ctx.n("5s", "2s")]
    if warmup:
        argv += ["-warmup", warmup]
    if cookie:
        argv += ["-cookie", cookie]
    if expect_body:
        argv += ["-expect-body", expect_body]
    for h in headers:
        argv += ["-header", h]
    rc, so, se = lib.run(argv, timeout=300, capture=True, check=False)
    try:
        res = json.loads(so)
    except ValueError:
        raise lib.Failure(f"loadgen printed no JSON (exit code {rc}):\n" + "\n".join((so + se).splitlines()[-8:]),
                          "run scripts/perf/loadgen by hand against the URL")
    if rc != 0 or res.get("errors"):
        raise lib.Failure(f"loadgen saw {res.get('errors')} failed requests of {res.get('requests')} at {url} "
                          f"(first: {res.get('first_error')})", "read the server logs and check the route")
    return res


def access_lines(log):
    """The number of access-log records in an API log file."""
    try:
        return sum(1 for line in log.read_text(errors="replace").splitlines() if ACCESS_LOG_MARK in line)
    except OSError:
        return 0


class NodeServer:
    """The SvelteKit Node server of a node build. stop() ends it."""

    def __init__(self, ctx, node, web, port, origin, api_base):
        self.ctx, self.node, self.web, self.origin, self.api_base = ctx, node, web, origin, api_base
        self.respawns = 0
        self._fh = None
        self._spawn(port)

    def _spawn(self, port):
        self.port = port
        self.log = self.ctx.tmp("node") / "web.log"
        # The values of Dockerfile.web, with a short shutdown so stop() is fast.
        env = web_env(self.ctx, NODE_ENV="production", HOST="127.0.0.1", PORT=port, PUBLIC_ORIGIN=self.origin,
                      API_URL=self.api_base, BODY_SIZE_LIMIT="1M", SHUTDOWN_TIMEOUT="1")
        argv = [self.node]
        if (self.web / INSTRUMENTATION).is_file():
            argv += ["--import", "./" + INSTRUMENTATION.as_posix()]
        argv.append("build")
        if self._fh:
            self._fh.close()
        self._fh = open(self.log, "w")
        self.t0 = time.perf_counter()
        self.proc = subprocess.Popen(argv, cwd=self.web, env=env, stdin=subprocess.DEVNULL, stdout=self._fh,
                                     stderr=subprocess.STDOUT, start_new_session=True)
        lib.track(self.proc.pid)

    def tail(self, n=15):
        try:
            return "\n".join(self.log.read_text(errors="replace").splitlines()[-n:])
        except OSError:
            return ""

    def wait_ready(self, timeout=60.0):
        """Polls /healthz every 5 ms. Returns ms since the spawn.

        Another process of the shared machine can take the port first. Then Node stops with
        EADDRINUSE, and wait_ready starts it again on a new port (at most 3 times). The build
        keeps the first origin, which only the CSRF check of form posts reads.
        """
        deadline = time.perf_counter() + timeout
        while True:
            if suite_app.healthz_ok(self.port):
                return (time.perf_counter() - self.t0) * 1000
            if self.proc.poll() is not None:
                if "EADDRINUSE" in self.tail(30) and self.respawns < 3:
                    lib.untrack(self.proc.pid)
                    self.respawns += 1
                    lib.log(f"the web port {self.port} was taken; starting Node again on a new port")
                    self._spawn(suite_app.free_port())
                    continue
                raise lib.Failure(f"the Node server stopped with exit code {self.proc.returncode} before /healthz "
                                  f"answered\n{self.tail()}", f"read {self.log}")
            if time.perf_counter() > deadline:
                raise lib.Failure(f"the Node server did not answer /healthz in {timeout:.0f} s\n{self.tail()}",
                                  f"read {self.log}")
            time.sleep(HEALTH_POLL_S)

    def stop(self):
        p = self.proc
        if p.poll() is None:
            try:
                os.killpg(p.pid, signal.SIGTERM)
            except (ProcessLookupError, PermissionError):
                pass
            try:
                p.wait(timeout=10)
            except subprocess.TimeoutExpired:
                try:
                    os.killpg(p.pid, signal.SIGKILL)
                except (ProcessLookupError, PermissionError):
                    pass
                p.wait()
        lib.untrack(p.pid)
        self._fh.close()


# ---------------------------------------------------------------------------
# Setup
# ---------------------------------------------------------------------------


def generate(ctx, parent, name, modules, frontend_mode=None, resource=True):
    """`spinup new` and (for node mode) the note resource with its web pages. Returns the app dir."""
    argv = [ctx.cli, "new", name, "--module", lib.APP_MODULE, "--modules", modules, "--no-git", "--non-interactive"]
    if frontend_mode:
        argv += ["--frontend-mode", frontend_mode]
    lib.run(argv, cwd=parent, env=web_env(ctx), timeout=900, capture=True)
    app = Path(parent) / name
    if resource:
        # Hide .env and DATABASE_URL, so the generator runs no migrate post-step.
        hidden = lib._hide_env(app)
        try:
            lib.run([ctx.cli, "g", "resource", *RESOURCE, "--non-interactive"], cwd=app, env=web_env(ctx),
                    timeout=900, capture=True)
        finally:
            lib._restore_env(hidden)
    return app


def pnpm_install(ctx, web):
    """Installs the web packages from the offline store. Failure with a Try: line when it cannot."""
    pnpm = ctx.repo / "scripts" / "bin" / "pnpm"
    rc, so, se = lib.run([pnpm, "install", "--frozen-lockfile", "--offline"], cwd=web, env=web_env(ctx),
                         timeout=900, capture=True, check=False)
    if rc != 0:
        tail = "\n".join((so + se).splitlines()[-10:])
        raise lib.Failure(f"pnpm install --frozen-lockfile --offline failed in {web} with exit code {rc}: the "
                          f"offline pnpm store does not have every package of the lockfile\n{tail}",
                          f"fill the store once with the network (cd {web} && pnpm install --frozen-lockfile), "
                          f"then run the suite again")


def build_api(ctx, app):
    """The release build of the API as the Dockerfile builds it. Returns the binary path."""
    out = ctx.tmp("api-bin") / "api"
    lib.run(["go", "build", "-trimpath", "-ldflags", "-s -w", "-o", out, "./cmd/api"], cwd=app / "api",
            env=ctx.cmd_env(CGO_ENABLED="0"), timeout=3600, capture=True)
    return out


def migrated_db(ctx, binary, prefix):
    """A new database with the migrations of binary. Returns its URL."""
    _, url = lib.create_db(ctx, prefix)
    env = ctx.cmd_env(DATABASE_URL=url, SPINUP_ENV="production", LOG_LEVEL="warn")
    env.pop("SPINUP_KIT_REPLACE", None)
    lib.run([binary, "migrate", "up"], cwd=ctx.tmp("mig"), env=env, timeout=300, capture=True)
    return url


def seed_notes(url, user_id):
    """Inserts SEED_ROWS notes with the title `perf note <n>` for the perf user."""
    lib.psql(url, f"INSERT INTO notes (user_id, title, body) SELECT {int(user_id)}, '{SEED_TITLE} ' || g, "
                  f"repeat('x', 200) FROM generate_series(1, {SEED_ROWS}) g")


class Scenario:
    """The node-mode app, its builds, and the running API."""

    def __init__(self, app, node):
        self.app = Path(app)
        self.web = self.app / "web"
        self.build = self.web / "build"
        self.node = node
        self.web_port = suite_app.free_port()  # PUBLIC_ORIGIN is fixed at build time
        self.origin = f"http://127.0.0.1:{self.web_port}"
        self.api = None
        self.cookie = None

    @property
    def api_base(self):
        return f"http://127.0.0.1:{self.api.port}"

    def build_web(self, ctx):
        pnpm = ctx.repo / "scripts" / "bin" / "pnpm"
        lib.run([pnpm, "build"], cwd=self.web, env=web_env(ctx, PUBLIC_ORIGIN=self.origin, API_URL=self.api_base),
                timeout=1800, capture=True)

    def node_server(self, ctx):
        return NodeServer(ctx, self.node, self.web, self.web_port, self.origin, self.api_base)


# ---------------------------------------------------------------------------
# Conformance
# ---------------------------------------------------------------------------


def fail(what, hint="read the server logs in the scratch directory"):
    raise lib.Failure(f"web conformance: {what}", hint)


def check_encoded_asset(port, path, label):
    """An immutable asset answers br, and a second request with If-None-Match answers 304."""
    ae = {"Accept-Encoding": "br, gzip"}
    st, hdr, _ = http_get(port, path, ae)
    if st != 200:
        fail(f"{label}: GET {path} answered {st}")
    if hdr.get("content-encoding") != "br":
        fail(f"{label}: GET {path} with Accept-Encoding: br answered content-encoding "
             f"{hdr.get('content-encoding')!r}, not br")
    return hdr


def check_revalidation(port, path, label, headers=None):
    """The answer has an ETag, and If-None-Match with it answers 304."""
    st, hdr, _ = http_get(port, path, headers)
    etag = hdr.get("etag")
    if st != 200 or not etag:
        fail(f"{label}: GET {path} answered {st} with ETag {etag!r}; a 200 with an ETag is required")
    st2, _, body = http_get(port, path, {**(headers or {}), "If-None-Match": etag})
    if st2 != 304:
        fail(f"{label}: GET {path} with If-None-Match {etag} answered {st2}, not 304")
    if body:
        fail(f"{label}: the 304 answer of {path} has a body of {len(body)} bytes")
    return etag


def node_conformance(port, cookie):
    """The checks of ADR 0034 section 2 against the Node server. Returns the sign-in page HTML."""
    st, hdr, body = http_get(port, PAGE_PATH, {"Cookie": cookie})
    if st != 200:
        fail(f"the signed-in GET {PAGE_PATH} answered {st}, not 200")
    if SEED_TITLE.encode() not in body:
        fail(f"the signed-in GET {PAGE_PATH} has no seeded title {SEED_TITLE!r}", "check the session forward of "
             "hooks.server.ts and the API log")
    csp = hdr.get("content-security-policy", "")
    tokens = [t.strip("'") for t in csp.replace(";", " ").split()]
    nonce = next((t[len("nonce-"):] for t in tokens if t.startswith("nonce-")), "")
    if not nonce:
        fail(f"GET {PAGE_PATH} sent no script nonce in its Content-Security-Policy ({csp!r})",
             "check the csp option of web/vite.config.js (ADR 0036 section 2)")
    if f'nonce="{nonce}"'.encode() not in body:
        fail(f"the inline script of {PAGE_PATH} does not carry the nonce of the Content-Security-Policy")
    st, _, login = http_get(port, LOGIN_PATH)
    if st != 200:
        fail(f"the anonymous GET {LOGIN_PATH} answered {st}, not 200")
    page = login.decode("utf-8", "replace")
    assets = [p for p in bundle.page_targets(page, LOGIN_PATH) if p.startswith("/_app/immutable/")]
    if not assets:
        fail(f"GET {LOGIN_PATH} loads no script from /_app/immutable/")
    check_encoded_asset(port, assets[0], "node")
    check_revalidation(port, assets[0], "node", {"Accept-Encoding": "br, gzip"})
    return page


def embed_conformance(port):
    """The checks of ADR 0034 section 1 against the embed binary."""
    ae = {"Accept-Encoding": "br, gzip"}
    st, hdr, body = http_get(port, "/", ae)
    if st != 200:
        fail(f"embed: GET / answered {st}, not 200")
    if hdr.get("content-encoding") != "br":
        fail(f"embed: GET / with Accept-Encoding: br answered content-encoding {hdr.get('content-encoding')!r}")
    csp = hdr.get("content-security-policy", "")
    script_src = next((d for d in csp.split(";") if d.strip().startswith("script-src")), "")
    if not script_src or "unsafe-inline" in script_src:
        fail(f"embed: the script-src of index.html is {script_src.strip()!r}; it must exist and not allow "
             f"'unsafe-inline'")
    check_revalidation(port, "/", "embed", ae)
    _, _, plain = http_get(port, "/")
    assets = [p for p in bundle.page_targets(plain.decode("utf-8", "replace"), "/")
              if p.startswith("/_app/immutable/")]
    if not assets:
        fail("embed: index.html loads no script from /_app/immutable/")
    check_encoded_asset(port, assets[0], "embed")


# ---------------------------------------------------------------------------
# Phase 2: node mode
# ---------------------------------------------------------------------------


def setup_node(ctx, parent, node, servers):
    """Generates and builds the node app, starts the API, signs in, and seeds. Returns the Scenario."""
    sc = Scenario(generate(ctx, parent, "app", MODULES), node)
    pnpm_install(ctx, sc.web)
    binary = build_api(ctx, sc.app)
    url = migrated_db(ctx, binary, "web")
    sc.api = suite_app.Server(ctx, binary, url, ctx.tmp("api-cwd"))
    servers.append(sc.api)
    sc.api.wait_ready()
    uid, sc.cookie = suite_app.sign_in(sc.api.port, register=True)
    seed_notes(url, uid)
    sc.build_web(ctx)
    return sc


def bundle_records(sc, recs, login_html):
    b = bundle.measure(sc.build)
    largest = ", ".join(f"{f['file']} {f['raw_bytes']}" for f in b["largest"])
    note = (f"scripts/perf/bundle.py measure: {b['files']} .js and .css files under _app/immutable of the client "
            f"build; the .br and .gz siblings are summed apart. Largest: {largest}")
    recs.append(lib.record(SUITE, "web_bundle_raw_bytes", "B", [b["raw_bytes"]], note=note,
                           metric_class="deterministic"))
    recs.append(lib.record(SUITE, "web_bundle_br_bytes", "B", [b["br_bytes"]], note=note,
                           metric_class="deterministic"))
    recs.append(lib.record(SUITE, "web_bundle_gz_bytes", "B", [b["gz_bytes"]], note=note,
                           metric_class="deterministic"))
    try:
        first, count = bundle.first_load_br_bytes(login_html, LOGIN_PATH, sc.build)
    except bundle.BundleError as e:
        raise lib.Failure(str(e), e.hint) from e
    recs.append(lib.record(SUITE, "web_first_load_br_bytes_login", "B", [first], metric_class="deterministic",
                           note=f"the brotli bytes of the {count} modulepreload and script targets of the anonymous "
                                f"GET {LOGIN_PATH} (the plain size when a file has no .br sibling); CSS not counted"))
    return b


def node_phase(ctx, sc, recs):
    srv = sc.node_server(ctx)
    try:
        srv.wait_ready()
        login_html = node_conformance(srv.port, sc.cookie)
    finally:
        srv.stop()
    lib.log("web conformance (node): passed")
    ctx.suite_meta[SUITE]["bundle"] = bundle_records(sc, recs, login_html)
    lib.write_results(ctx)

    def build_fn(i, warm):
        t = time.perf_counter()
        sc.build_web(ctx)
        return time.perf_counter() - t

    recs.append(lib.measure(
        ctx, SUITE, "web_build_time", build_fn, n=ctx.n(3, 1), unit="s", warmup=0,
        note="pnpm build (vite build with adapter-node and precompress) of the generated app after the setup "
             "build, with the packages installed"))
    # The build is deterministic, so the bytes must match the first measure.
    again = bundle.measure(sc.build)
    if again["raw_bytes"] != ctx.suite_meta[SUITE]["bundle"]["raw_bytes"]:
        ctx.notes.append("web suite: a second build changed the client bundle bytes; the bundle records come "
                         "from the first build")

    def start_fn(i, warm):
        s = sc.node_server(ctx)
        try:
            return s.wait_ready()
        finally:
            s.stop()

    recs.append(lib.measure(
        ctx, SUITE, "web_start_to_healthz", start_fn, n=ctx.n(10, 3), unit="ms",
        note="node --import <instrumentation> build (the start command of Dockerfile.web), NODE_ENV=production; "
             "spawn to the first 200 from /healthz, polled every 5 ms with a new connection"))

    keys = ("idle", "after", "login_rps", "ttfb",
            *[f"p_{p}" for p in ("p50", "p90", "p99", "p999")], *[f"l_{p}" for p in ("p50", "p90", "p99", "p999")])
    side = {k: [] for k in keys}

    def load_fn(i, warm):
        s = sc.node_server(ctx)
        try:
            s.wait_ready()
            time.sleep(IDLE_WAIT_S)
            idle = suite_app.rss_mib(s.proc.pid)
            base = f"http://127.0.0.1:{s.port}"
            page = loadgen(ctx, base + PAGE_PATH, cookie=sc.cookie, expect_body=SEED_TITLE)
            login = loadgen(ctx, base + LOGIN_PATH, expect_body="<form")
            after = suite_app.rss_mib(s.proc.pid)
            ttfb = loadgen(ctx, base + PAGE_PATH, concurrency=1, cookie=sc.cookie, expect_body=SEED_TITLE)
        finally:
            s.stop()
        if not warm:
            side["idle"].append(idle)
            side["after"].append(after)
            side["login_rps"].append(login["rps"])
            side["ttfb"].append(ttfb["ttfb_ms"]["p50"])
            for p in ("p50", "p90", "p99", "p999"):
                side[f"p_{p}"].append(page["latency_ms"][p])
                side[f"l_{p}"].append(login["latency_ms"][p])
        return page["rps"]

    lg_note = (f"scripts/perf/loadgen -c {CONCURRENCY} -d {ctx.n('5s', '2s')} -warmup 1s (keep-alive, no "
               f"compression, the client runs on the same machine) against one Node process; one new Node server "
               f"for each sample; the API runs on the same machine")
    page_note = (f"GET {PAGE_PATH} with the session cookie: SSR of the list page (20 of {SEED_ROWS} seeded notes), "
                 f"one /api/session and one /api/notes call to the Go API. " + lg_note)
    login_note = f"anonymous GET {LOGIN_PATH}: SSR of the sign-in page, no API call. " + lg_note
    primary = lib.measure(ctx, SUITE, "web_ssr_rps_page", load_fn, n=ctx.n(3, 1), unit="req/s", better="higher",
                          note=page_note)
    recs.append(primary)
    fails = [p for p in primary["note"].split("; ") if p.startswith("failed at") or p.startswith("load_gate")]

    def side_rec(metric, key, unit, better, note, info=None):
        recs.append(lib.record(SUITE, metric, unit, side[key], better=better, warmup=1,
                               note="; ".join([note, *fails]), informational=info))

    side_rec("web_ssr_rps_login", "login_rps", "req/s", "higher", login_note)
    for p in ("p50", "p90", "p99", "p999"):
        side_rec(f"web_latency_{p}_page", f"p_{p}", "ms", "lower", page_note, LATENCY_INFO)
        side_rec(f"web_latency_{p}_login", f"l_{p}", "ms", "lower", login_note, LATENCY_INFO)
    side_rec("web_ttfb_page", "ttfb", "ms", "lower",
             f"p50 time to first byte of GET {PAGE_PATH} with loadgen -c 1 -d {ctx.n('5s', '2s')} after the RSS "
             f"reading", TTFB_INFO)
    side_rec("web_idle_rss", "idle", "MiB", "lower",
             f"ps RSS of the Node process {IDLE_WAIT_S:.0f} s after the first 200 from /healthz, before any load")
    side_rec("web_rss_after_load", "after", "MiB", "lower",
             "ps RSS of the Node process right after the page and the sign-in load runs")
    lib.write_results(ctx)

    s = sc.node_server(ctx)
    try:
        s.wait_ready()
        time.sleep(0.2)
        before = access_lines(sc.api.log)
        res = loadgen(ctx, f"http://127.0.0.1:{s.port}{PAGE_PATH}", concurrency=COUNT_CONCURRENCY,
                      cookie=sc.cookie, expect_body=SEED_TITLE, requests=COUNT_REQUESTS, warmup=None)
        time.sleep(0.5)
        lines = access_lines(sc.api.log) - before
    finally:
        s.stop()
    if lines == 0:
        fail("the API access log has no records after the signed-in page load")
    recs.append(lib.record(
        SUITE, "web_api_requests_per_page", "req", [lines / res["requests"]], metric_class="deterministic",
        note=f"API access-log records ({lines}) for each signed-in GET {PAGE_PATH} ({res['requests']}) of a page "
             f"load run with loadgen -n {COUNT_REQUESTS} -c {COUNT_CONCURRENCY} and no warm-up"))


# ---------------------------------------------------------------------------
# Phase 3: embed mode
# ---------------------------------------------------------------------------

EMBED_METRICS = [("embed_binary_size", "B", "lower", "deterministic"), ("embed_index_rps", "req/s", "higher", None)]


def embed_phase(ctx, parent, recs, servers):
    app = generate(ctx, parent, "emb", EMBED_MODULES, frontend_mode="embed", resource=False)
    pnpm_install(ctx, app / "web")
    lib.run([ctx.cli, "build"], cwd=app, env=web_env(ctx), timeout=3600, capture=True)
    binary = app / "api" / "bin" / "api"
    if not binary.is_file():
        raise lib.Failure(f"spinup build wrote no {binary}", f"run spinup build by hand in {app}")
    url = migrated_db(ctx, binary, "embed")
    srv = suite_app.Server(ctx, binary, url, ctx.tmp("embed-cwd"))
    servers.append(srv)
    srv.wait_ready()
    embed_conformance(srv.port)
    lib.log("web conformance (embed): passed")
    recs.append(lib.record(SUITE, "embed_binary_size", "B", [binary.stat().st_size], metric_class="deterministic",
                           note="api/bin/api of spinup build in an embed app (modules ci; go build -tags embedweb "
                                "-trimpath, not stripped) with the precompressed web files inside"))
    base = f"http://127.0.0.1:{srv.port}"

    def index_fn(i, warm):
        return loadgen(ctx, base + "/", headers=["Accept-Encoding: br, gzip"])["rps"]

    recs.append(lib.measure(
        ctx, SUITE, "embed_index_rps", index_fn, n=ctx.n(3, 1), unit="req/s", better="higher",
        note=f"GET / with Accept-Encoding: br, gzip against the embed binary (SPINUP_ENV=production, LOG_LEVEL=info): "
             f"index.html.br from the embedded file index. loadgen -c {CONCURRENCY} -d {ctx.n('5s', '2s')} "
             f"-warmup 1s; one server for all samples"))


# ---------------------------------------------------------------------------
# Entry
# ---------------------------------------------------------------------------


def run(ctx):
    recs = ctx.live
    meta = ctx.suite_meta.setdefault(SUITE, {})
    node, version = node_binary(ctx)
    meta["node"] = version
    parent = ctx.tmp("web")
    servers = []
    try:
        sc = setup_node(ctx, parent, node, servers)
        node_phase(ctx, sc, recs)
        lib.write_results(ctx)
        if ctx.quick:
            for metric, unit, better, cls in EMBED_METRICS:
                recs.append(lib.skipped(SUITE, metric, unit, "skipped with --quick", better=better,
                                        metric_class=cls))
        else:
            embed_phase(ctx, parent, recs, servers)
            lib.write_results(ctx)
    finally:
        for s in servers:
            s.stop()
        suite_app.rmtree(parent)
    return recs
