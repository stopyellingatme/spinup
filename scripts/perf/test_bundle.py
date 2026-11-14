"""Tests for scripts/perf/bundle.py."""

import contextlib
import importlib.util
import io
import json
import tempfile
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent


def load(name):
    spec = importlib.util.spec_from_file_location(name, HERE / (name + ".py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


bundle = load("bundle")


def write(root, rel, size):
    p = Path(root) / rel
    p.parent.mkdir(parents=True, exist_ok=True)
    p.write_bytes(b"x" * size)


def node_build(root):
    """A node build: client files under client/, server files that never count."""
    imm = "client/_app/immutable/"
    write(root, imm + "entry/start.js", 1000)
    write(root, imm + "entry/start.js.br", 300)
    write(root, imm + "entry/start.js.gz", 400)
    write(root, imm + "assets/0.css", 500)
    write(root, imm + "assets/0.css.br", 100)
    write(root, imm + "assets/0.css.gz", 150)
    write(root, imm + "chunks/tiny.js", 20)  # no siblings: the plain size counts
    write(root, imm + "assets/logo.svg", 9999)  # not .js or .css
    write(root, "client/_app/version.json", 50)  # not under immutable
    write(root, "server/index.js", 77777)
    write(root, "index.js", 6666)


def run_main(argv):
    out, err = io.StringIO(), io.StringIO()
    with contextlib.redirect_stdout(out), contextlib.redirect_stderr(err):
        rc = bundle.main(argv)
    return rc, out.getvalue(), err.getvalue()


class MeasureTests(unittest.TestCase):
    def test_node_build_sums_siblings_apart(self):
        with tempfile.TemporaryDirectory() as d:
            node_build(d)
            r = bundle.measure(d)
            self.assertEqual(r["files"], 3)
            self.assertEqual(r["raw_bytes"], 1520)
            self.assertEqual(r["br_bytes"], 420)
            self.assertEqual(r["gz_bytes"], 570)
            self.assertEqual([f["path"] for f in r["largest"]],
                             ["_app/immutable/entry/start.js", "_app/immutable/assets/0.css",
                              "_app/immutable/chunks/tiny.js"])

    def test_embed_build_has_no_client_dir(self):
        with tempfile.TemporaryDirectory() as d:
            write(d, "_app/immutable/a.js", 10)
            write(d, "_app/immutable/a.js.br", 4)
            write(d, "index.html", 100)
            r = bundle.measure(d)
            self.assertEqual((r["raw_bytes"], r["br_bytes"], r["gz_bytes"]), (10, 4, 10))

    def test_largest_keeps_five(self):
        with tempfile.TemporaryDirectory() as d:
            for i in range(8):
                write(d, f"_app/immutable/c{i}.js", 10 + i)
            r = bundle.measure(d)
            self.assertEqual([f["raw_bytes"] for f in r["largest"]], [17, 16, 15, 14, 13])

    def test_missing_build_is_an_error(self):
        with tempfile.TemporaryDirectory() as d:
            with self.assertRaises(bundle.BundleError):
                bundle.measure(Path(d) / "nope")
            with self.assertRaises(bundle.BundleError):
                bundle.measure(d)

    def test_measure_command_prints_json(self):
        with tempfile.TemporaryDirectory() as d:
            node_build(d)
            rc, out, _ = run_main(["measure", d])
            self.assertEqual(rc, 0)
            self.assertEqual(json.loads(out)["br_bytes"], 420)


class CheckTests(unittest.TestCase):
    def budgets(self, d, data):
        p = Path(d) / "budgets.json"
        p.write_text(data if isinstance(data, str) else json.dumps(data))
        return str(p)

    def test_in_budget_prints_one_line(self):
        with tempfile.TemporaryDirectory() as d:
            node_build(d)
            rc, out, err = run_main(["check", d, self.budgets(d, {"web_bundle_raw_bytes": 1520,
                                                                   "web_bundle_br_bytes": 420})])
            self.assertEqual(rc, 0, err)
            self.assertEqual(len(out.strip().splitlines()), 1)
            self.assertIn("in budget", out)

    def test_missing_key_means_no_limit(self):
        with tempfile.TemporaryDirectory() as d:
            node_build(d)
            rc, out, _ = run_main(["check", d, self.budgets(d, {"other": 1})])
            self.assertEqual(rc, 0)
            self.assertIn("no limit", out)

    def test_raw_above_budget_fails(self):
        with tempfile.TemporaryDirectory() as d:
            node_build(d)
            rc, _, err = run_main(["check", d, self.budgets(d, {"web_bundle_raw_bytes": 1519})])
            self.assertEqual(rc, 1)
            self.assertIn("raw_bytes 1,520 is above web_bundle_raw_bytes 1,519", err)
            self.assertIn("Try:", err)

    def test_br_above_budget_fails(self):
        with tempfile.TemporaryDirectory() as d:
            node_build(d)
            rc, _, err = run_main(["check", d, self.budgets(d, {"web_bundle_br_bytes": 100})])
            self.assertEqual(rc, 1)
            self.assertIn("br_bytes 420 is above web_bundle_br_bytes 100", err)

    def test_bad_budgets_are_usage_errors(self):
        with tempfile.TemporaryDirectory() as d:
            node_build(d)
            for data in ("{", "[]", {"web_bundle_br_bytes": -1}, {"web_bundle_raw_bytes": "big"},
                         {"web_bundle_raw_bytes": True}):
                rc, _, err = run_main(["check", d, self.budgets(d, data)])
                self.assertEqual(rc, 2, data)
                self.assertIn("Try:", err)
            rc, _, err = run_main(["check", d, str(Path(d) / "missing.json")])
            self.assertEqual(rc, 2)

    def test_usage(self):
        self.assertEqual(run_main([])[0], 2)
        self.assertEqual(run_main(["measure"])[0], 2)
        self.assertEqual(run_main(["check", "x"])[0], 2)
        self.assertEqual(run_main(["--help"])[0], 0)


PAGE = """<!doctype html><html><head>
<link href="./_app/immutable/entry/start.js" rel="modulepreload">
<link href="./_app/immutable/chunks/tiny.js" rel="modulepreload">
<link href="./_app/immutable/assets/0.css" rel="stylesheet">
<link href="https://cdn.example.com/x.js" rel="modulepreload">
</head><body>
<script nonce="abc">
  import("./_app/immutable/entry/start.js").then(async (kit) => {
    const app = await import("./_app/immutable/entry/app.js");
  });
</script>
<script src="/_app/immutable/chunks/tiny.js"></script>
</body></html>"""


class FirstLoadTests(unittest.TestCase):
    def test_targets_are_resolved_and_unique(self):
        self.assertEqual(bundle.page_targets(PAGE, "/login"),
                         ["/_app/immutable/entry/start.js", "/_app/immutable/chunks/tiny.js",
                          "/_app/immutable/entry/app.js"])

    def test_first_load_sums_brotli_siblings(self):
        with tempfile.TemporaryDirectory() as d:
            node_build(d)
            write(d, "client/_app/immutable/entry/app.js", 900)
            write(d, "client/_app/immutable/entry/app.js.br", 200)
            self.assertEqual(bundle.first_load_br_bytes(PAGE, "/login", d), (300 + 20 + 200, 3))

    def test_first_load_missing_file_is_an_error(self):
        with tempfile.TemporaryDirectory() as d:
            node_build(d)
            with self.assertRaises(bundle.BundleError):
                bundle.first_load_br_bytes(PAGE, "/login", d)


if __name__ == "__main__":
    unittest.main()
