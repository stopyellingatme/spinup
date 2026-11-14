"""Tests for the standard-library performance helpers."""

import importlib.util
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


lib = load("lib")
compare = load("compare")
perf = load("perf")


class RecordTests(unittest.TestCase):
    def test_record_has_metric_class(self):
        self.assertEqual(lib.record("cli", "time", "ms", [1])["class"], "primary")
        self.assertEqual(
            lib.record("app", "latency", "ms", [1], informational="noisy")["class"],
            "informational",
        )
        self.assertEqual(
            lib.record("micro", "allocation", "allocs/op", [1])["class"],
            "deterministic",
        )


class CompareTests(unittest.TestCase):
    def test_deterministic_regression_fails_on_different_cpus(self):
        base = {"meta": {"machine": {"cpu": "one"}}, "records": [
            lib.record("micro", "allocation", "allocs/op", [1]),
        ]}
        new = {"meta": {"machine": {"cpu": "two"}}, "records": [
            lib.record("micro", "allocation", "allocs/op", [2]),
        ]}
        rows, regressions = compare.compare(base, new, 0.1, only="deterministic")
        self.assertEqual(len(rows), 1)
        self.assertEqual(regressions, ["micro/allocation (deterministic)"])


class BaselineTests(unittest.TestCase):
    def test_baseline_removes_machine_and_repository_identity(self):
        data = {
            "meta": {
                "quick": False,
                "machine": {"cpu": "A CPU!", "cores": 4, "ram": "8 GiB", "os": "test", "hostname": "host"},
                "repo": {"sha": "abc", "path": ""},
            },
            "gate_events": [],
            "nested": {"hostname": "also-host", "path": "/also-secret"},
            "records": [],
        }
        with tempfile.TemporaryDirectory() as d:
            source = Path(d) / "result.json"
            source.write_text(json.dumps(data))
            destination = Path(d) / "repo"
            destination.mkdir()
            data["meta"]["repo"]["path"] = str(destination)
            data["note"] = f"{destination}/a host says host"
            data["meta_args"] = {"out": f"{destination}/output.json"}
            source.write_text(json.dumps(data))
            json_path, markdown_path = perf.write_baseline(source, destination)
            written = json.loads(json_path.read_text())
            self.assertEqual(json_path.name, "a-cpu-.json")
            self.assertTrue(markdown_path.is_file())
            self.assertNotIn("hostname", written["meta"]["machine"])
            self.assertNotIn("path", written["meta"]["repo"])
            self.assertNotIn("hostname", written["nested"])
            self.assertNotIn("path", written["nested"])
            self.assertEqual(written["meta_args"]["out"], "<repo>/output.json")
            self.assertEqual(written["note"], "<repo>/a <host> says <host>")

    def test_baseline_keeps_bundle_file_without_a_build_directory(self):
        with tempfile.TemporaryDirectory() as d:
            source = Path(d) / "result.json"
            destination = Path(d) / "repo"
            destination.mkdir()
            data = {
                "meta": {"quick": False, "machine": {"cpu": "CPU"}, "repo": {"path": str(destination)}},
                "gate_events": [],
                "suite_meta": {"web": {"bundle": {"largest": [
                    {"file": "_app/immutable/entry/start.js", "raw_bytes": 42},
                ]}}},
                "records": [],
            }
            source.write_text(json.dumps(data))
            json_path, _ = perf.write_baseline(source, destination)
            written = json.loads(json_path.read_text())
            bundle = written["suite_meta"]["web"]["bundle"]
            self.assertNotIn("dir", bundle)
            self.assertEqual(bundle["largest"][0]["file"], "_app/immutable/entry/start.js")

    def test_baseline_rejects_quick_result(self):
        data = {"meta": {"quick": True, "machine": {"cpu": "CPU"}, "repo": {}}, "gate_events": [],
                "records": []}
        with tempfile.TemporaryDirectory() as d:
            source = Path(d) / "result.json"
            source.write_text(json.dumps(data))
            with self.assertRaises(Exception) as raised:
                perf.write_baseline(source, Path(d))
            self.assertIn("quick run", str(raised.exception))


suite_app = load("suite_app")

LIVEGEN_OK = {
    "conformance": {"ok": True},
    "fanout": {"latency_ms": {"p50": 1.5, "p90": 2.0, "p99": 4.25, "max": 9.0}, "samples": 1000, "early": 3,
               "missed": 0, "duplicates": 0, "out_of_order": 0, "mismatched": 0, "order_ok": True},
    "memory": {"before_kib": 20000, "after_kib": 40000, "per_stream_kib": 20.0},
    "resume": {"ready_s": 0.8, "resumed": 100, "sync": 0, "status_503": 2, "status_429": 0, "retries": 2,
               "max_attempts": 2},
    "drain": {"ended_s": 0.05, "exit_s": 0.3, "bye": 100},
    "failures": [],
}


class LivePhaseTests(unittest.TestCase):
    def test_live_phase_runs_only_with_perf_live(self):
        self.assertNotIn(suite_app.live_phase, suite_app.phases({}))
        self.assertNotIn(suite_app.live_phase, suite_app.phases({"PERF_LIVE": "0"}))
        self.assertEqual(suite_app.phases({"PERF_LIVE": "1"})[-1], suite_app.live_phase)

    def test_live_records_from_livegen_json(self):
        recs, failures = suite_app.live_records(LIVEGEN_OK, 100, 10)
        self.assertEqual(failures, [])
        by = {r["metric"]: r for r in recs}
        self.assertEqual(sorted(by), sorted(m for m, *_ in suite_app.LIVE_METRICS))
        self.assertEqual(by["live_fanout_p50_ms"]["median"], 1.5)
        self.assertEqual(by["live_fanout_p99_ms"]["median"], 4.25)
        self.assertEqual(by["live_fanout_p99_ms"]["class"], "primary")
        self.assertEqual(by["live_stream_kib"]["median"], 20.0)
        self.assertEqual(by["live_stream_kib"]["class"], "primary")
        self.assertEqual(by["live_resume_ready_s"]["median"], 0.8)
        self.assertEqual(by["live_resume_sync_count"]["median"], 0)
        self.assertEqual(by["live_resume_503_count"]["median"], 2)
        self.assertEqual(by["live_drain_s"]["median"], 0.05)
        self.assertEqual(by["live_drain_exit_s"]["median"], 0.3)
        self.assertTrue(by["live_drain_s"]["informational"])
        for r in recs:
            self.assertEqual(r["category"], "live")
            self.assertEqual(r["suite"], "app")
            self.assertIn("N=100", r["note"])

    def test_failed_conformance_records_no_zero(self):
        recs, failures = suite_app.live_records({"conformance": {"ok": False, "error": "no ready"}}, 100, 10)
        self.assertEqual(len(failures), 1)
        self.assertIn("no ready", failures[0])
        for r in recs:
            self.assertEqual(r["samples"], [])
            self.assertIsNone(r["median"])

    def test_missed_patch_fails_the_fanout(self):
        res = json.loads(json.dumps(LIVEGEN_OK))
        res["fanout"]["missed"] = 2
        res["fanout"]["order_ok"] = True
        res["drain"]["error"] = "3 streams ended without a bye event"
        recs, failures = suite_app.live_records(res, 100, 10)
        by = {r["metric"]: r for r in recs}
        self.assertEqual(len(failures), 2)
        self.assertIsNone(by["live_fanout_p99_ms"]["median"])
        self.assertIsNone(by["live_drain_s"]["median"])
        self.assertEqual(by["live_stream_kib"]["median"], 20.0)


if __name__ == "__main__":
    unittest.main()
