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
                "repo": {"sha": "abc", "path": "/secret/repo"},
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
            json_path, markdown_path = perf.write_baseline(source, destination)
            written = json.loads(json_path.read_text())
            self.assertEqual(json_path.name, "a-cpu-.json")
            self.assertTrue(markdown_path.is_file())
            self.assertNotIn("hostname", written["meta"]["machine"])
            self.assertNotIn("path", written["meta"]["repo"])
            self.assertNotIn("hostname", written["nested"])
            self.assertNotIn("path", written["nested"])

    def test_baseline_rejects_quick_result(self):
        data = {"meta": {"quick": True, "machine": {"cpu": "CPU"}, "repo": {}}, "gate_events": [],
                "records": []}
        with tempfile.TemporaryDirectory() as d:
            source = Path(d) / "result.json"
            source.write_text(json.dumps(data))
            with self.assertRaises(Exception) as raised:
                perf.write_baseline(source, Path(d))
            self.assertIn("quick run", str(raised.exception))


if __name__ == "__main__":
    unittest.main()
