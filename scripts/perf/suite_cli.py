"""The cli suite: binary size, start-up, generation, upgrade, migration, and the peak RSS of new."""

import shutil
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import lib  # noqa: E402

SUITE = "cli"
FIRST_RESOURCE = "todo title:string done:bool due:date? priority:enum:low|normal|high"


def _new_args():
    return ["new", "app", "--module", lib.APP_MODULE, "--modules", lib.CLI_MODULES, "--non-interactive",
            "--no-git", "--api-only"]


def _cmd_fn(ctx, args):
    return lambda i, warm: lib.cmd_ms([ctx.cli, *args], env=ctx.cmd_env())


def run(ctx):
    recs = ctx.live  # perf.py saves it with every write_results, so a crash keeps the partial suite
    resources = ctx.resources

    recs.append(lib.record(SUITE, "cli_binary_size", "MB", [ctx.cli_size / 1e6],
                           note="the stripped CLI (go build -trimpath -ldflags '-s -w' ./cmd/spinup)"))

    recs.append(lib.measure(ctx, SUITE, "help_time", _cmd_fn(ctx, ["--help"]), n=ctx.n(30, 5),
                            note="spinup --help, process wall time"))
    recs.append(lib.measure(ctx, SUITE, "version_time", _cmd_fn(ctx, ["version"]), n=ctx.n(30, 5),
                            note="spinup version, process wall time"))

    def new_fn(i, warm):
        d = ctx.tmp("new")
        try:
            return lib.cmd_ms([ctx.cli, *_new_args()], env=ctx.cmd_env(), cwd=d)
        finally:
            shutil.rmtree(d, ignore_errors=True)

    recs.append(lib.measure(ctx, SUITE, "new_time", new_fn, n=ctx.n(10, 3),
                            note=f"spinup new --api-only --modules {lib.CLI_MODULES} --no-git in an empty dir"))

    nodb = ("generation only: the .env files are hidden and DATABASE_URL is unset, so `g resource` skips "
            "its database post-steps")

    def one_fn(i, warm):
        d = ctx.tmp("gen1")
        try:
            app = lib.new_app(ctx, d)
            return lib.gen_resources(ctx, app, [FIRST_RESOURCE])[0]
        finally:
            shutil.rmtree(d, ignore_errors=True)

    recs.append(lib.measure(ctx, SUITE, "gen_one_resource_nodb_time", one_fn, n=ctx.n(10, 3),
                            note=f"spinup g resource {FIRST_RESOURCE} --api-only in a new API-only app (the app "
                                 f"is created before the timer); {nodb}"))

    def gen_fn(i, warm):
        d = ctx.tmp("gen")
        try:
            app = lib.new_app(ctx, d)
            return sum(lib.gen_resources(ctx, app, resources)) / 1000
        finally:
            shutil.rmtree(d, ignore_errors=True)

    recs.append(lib.measure(ctx, SUITE, "gen_resources_nodb_time", gen_fn, n=ctx.n(3, 1), unit="s",
                            warmup=ctx.n(1, 0),
                            note=f"spinup g resource --api-only for the {len(resources)} resources of "
                                 f"scripts/perf/resources.txt, summed process time (the app is created before "
                                 f"the timer); {nodb}"))

    # Upgrade, no change. The app needs a git repository and a clean tree.
    try:
        up_app = lib.new_app(ctx, ctx.tmp("upgrade"), git=True)
    except lib.Failure as e:
        recs.append(lib.skipped(SUITE, "upgrade_noop_time", "ms", f"could not create the app: {e}"))
    else:
        recs.append(lib.measure(
            ctx, SUITE, "upgrade_noop_time",
            lambda i, warm: lib.cmd_ms([ctx.cli, "upgrade", "--offline", "--non-interactive"], env=ctx.cmd_env(),
                                       cwd=up_app),
            n=ctx.n(10, 3), note="spinup upgrade --offline on a new git app with no template change"))
        shutil.rmtree(up_app.parent, ignore_errors=True)

    # db migrate on the scenario app against a new database for each sample.
    try:
        mig_app = lib.gen_app(ctx, ctx.tmp("migrate"))["app"]
    except lib.Failure as e:
        recs.append(lib.skipped(SUITE, "db_migrate_time", "ms", f"could not create the app: {e}"))
    else:
        def mig_fn(i, warm):
            name, url = lib.create_db(ctx, "mig")
            try:
                return lib.cmd_ms([ctx.cli, "db", "migrate", "--non-interactive"], env=ctx.cmd_env(DATABASE_URL=url),
                                  cwd=mig_app, timeout=1800)
            finally:
                lib.drop_db(ctx, name)

        recs.append(lib.measure(
            ctx, SUITE, "db_migrate_time", mig_fn, n=ctx.n(5, 2),
            note=f"spinup db migrate on the {len(resources)}-resource API-only app, a new database for each "
                 f"sample. It runs `go run ./cmd/api migrate up`, so the link is included (the build cache is "
                 f"warm after the warm-up)"))
        shutil.rmtree(mig_app.parent, ignore_errors=True)

    def rss_fn(i, warm):
        d = ctx.tmp("rss")
        try:
            v = lib.peak_rss_mib([ctx.cli, *_new_args()], env=ctx.cmd_env(), cwd=d)
        finally:
            shutil.rmtree(d, ignore_errors=True)
        if v is None:
            raise lib.Failure("peak RSS is not measurable here (/usr/bin/time -l or -v is missing)")
        return v

    recs.append(lib.measure(ctx, SUITE, "peak_rss_new", rss_fn, n=ctx.n(5, 2), unit="MiB",
                            note="maximum resident set size of spinup new --api-only, /usr/bin/time "
                                 "(children included)"))
    return recs
