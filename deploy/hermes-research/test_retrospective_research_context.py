import copy
import hashlib
import importlib.util
import os
from pathlib import Path
import shlex
import subprocess
import sys
import tempfile
import unittest


HERE = Path(__file__).parent
SPEC = importlib.util.spec_from_file_location("retrospective_context", HERE / "historical-research-context.py")
context = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(context)


def fixture():
    lane = {"counts": {"ticks": 5477, "sells": 0, "buys": 0, "refused": 0,
                       "filtered": 116, "missed": 0, "sell_signals": 116, "buy_signals": 0},
            "filtered_reasons": {"signal_below_cost_hurdle": 116},
            "equity_micros": "49491015", "versus_hold_micros": "-12", "max_drawdown_micros": "1041264"}
    candidate = copy.deepcopy(lane)
    candidate["counts"].update(filtered=49, sell_signals=49)
    candidate["filtered_reasons"]["signal_below_cost_hurdle"] = 49
    return {"version": 1, "kind": "retrospective_training", "paper_only": True,
            "authorized": False, "promotable": False, "pool_modelled": True,
            "market": "SOL/USDC", "spread_bps": 100, "hypothesis_created_at": "2026-09-07T13:19:15Z",
            "packet_sha256": "a" * 64, "base_policy_sha256": "b" * 64,
            "candidate_policy_sha256": "c" * 64, "recorded_basis_sha256": "d" * 64,
            "parameter_changes": [{"name": "minimum_signal_bps", "current": 20, "proposed": 25}],
            "journal": {"day": "2026-09-06", "records": 5479, "chain_head_sha256": "e" * 64},
            "base": lane, "candidate": candidate, "rationale": "PRIVATE-PROSE /private/path"}


class RetrospectiveContextTest(unittest.TestCase):
    def project(self, report=None, raw=None, pin=None, packet_pin="a" * 64):
        raw = context.canonical(fixture() if report is None else report) if raw is None else raw
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "report.json"
            path.write_bytes(raw)
            path.chmod(0o600)
            return context.project(path, pin or hashlib.sha256(raw).hexdigest(), "packet-backtest", packet_pin)

    def test_go_report_projection(self):
        result = self.project()
        self.assertEqual(result["parameter_changes"], fixture()["parameter_changes"])
        self.assertEqual(result["candidate"]["counts"]["filtered"], 49)
        self.assertEqual(result["base"]["versus_hold_micros"], "-12")
        self.assertFalse(result["financial_results_independently_verified"])
        self.assertFalse(result["recorded_basis_eligible"])
        self.assertFalse(result["authorized"])
        self.assertNotIn(b"PRIVATE-PROSE", context.canonical(result))
        report = fixture()
        for name in ("base", "candidate"):
            report[name]["counts"]["filtered"] = 0
            report[name]["filtered_reasons"] = None
        self.assertEqual(self.project(report)["base"]["filtered_reasons"], {})

    def test_invalid_pins_and_json(self):
        for kwargs in ({"pin": "0" * 64}, {"packet_pin": "0" * 64}, {"packet_pin": None},
                       {"raw": b'{"a":1,"a":2}'}, {"raw": b'{"a":NaN}'},
                       {"raw": b'{"a":1e999}'}, {"raw": b'[]'}):
            with self.subTest(kwargs=kwargs), self.assertRaises((ValueError, TypeError)):
                self.project(**kwargs)

    def test_invalid_report_fields(self):
        changes = [
            ("version", True), ("kind", "validation"), ("market", "PRIVATE"), ("spread_bps", 0),
            ("spread_bps", True), ("authorized", True), ("paper_only", 1),
            ("hypothesis_created_at", "2099-01-02T00:00:00Z"),
            ("hypothesis_created_at", "2026-09-06T00:00:00Z"),
            ("hypothesis_created_at", "2026-09-07T00:00:00+00:00"),
            ("base_policy_sha256", "PRIVATE"), ("parameter_changes", []),
        ]
        for key, value in changes:
            report = fixture(); report[key] = value
            with self.subTest(key=key, value=value), self.assertRaises((ValueError, TypeError)):
                self.project(report)
        for case in ("parameter", "same", "duplicate", "parameter-bool", "parameter-range", "reason", "reason-count",
                     "ticks", "count-bool", "money-bool", "money-leading-zero", "money-overflow", "missing", "day", "older-day"):
            report = fixture()
            if case == "parameter": report["parameter_changes"][0]["name"] = "PRIVATE"
            elif case == "same": report["parameter_changes"][0]["proposed"] = 20
            elif case == "duplicate": report["parameter_changes"] *= 2
            elif case == "parameter-bool": report["parameter_changes"][0]["current"] = True
            elif case == "parameter-range": report["parameter_changes"][0]["proposed"] = 5001
            elif case == "reason": report["base"]["filtered_reasons"] = {"PRIVATE": 116}
            elif case == "reason-count": report["base"]["filtered_reasons"]["signal_below_cost_hurdle"] = 115
            elif case == "ticks": report["candidate"]["counts"]["ticks"] = 5476
            elif case == "count-bool": report["base"]["counts"]["sells"] = True
            elif case == "money-bool": report["base"]["equity_micros"] = True
            elif case == "money-leading-zero": report["base"]["equity_micros"] = "01"
            elif case == "money-overflow": report["base"]["equity_micros"] = str(2**64)
            elif case == "missing": del report["journal"]
            elif case == "day": report["journal"]["day"] = "2026-9-6"
            elif case == "older-day": report["journal"]["day"] = "2026-09-05"
            with self.subTest(case=case), self.assertRaises((ValueError, TypeError, KeyError)):
                self.project(report)

    def test_private_file_boundary(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "report"
            raw = context.canonical(fixture()); path.write_bytes(raw); path.chmod(0o644)
            pin = hashlib.sha256(raw).hexdigest()
            with self.assertRaises(ValueError): context.project(path, pin, "packet-backtest", "a" * 64)
            path.chmod(0o600)
            link = Path(directory) / "link"; link.symlink_to(path)
            with self.assertRaises(ValueError): context.project(link, pin, "packet-backtest", "a" * 64)

    def test_actual_wrapper_with_real_reader(self):
        wrapper = (HERE / "run-market-scout.sh").read_text()
        start = wrapper.index("  retrospective_research=unavailable\n")
        block = wrapper[start:wrapper.index("  historical_research=unavailable\n", start)]
        for mode in ("success", "wrong-pin", "missing-report-pin", "missing-packet-pin", "failed-reader"):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as directory:
                report = Path(directory) / "report.json"
                raw = context.canonical(fixture()); report.write_bytes(raw); report.chmod(0o600)
                digest = hashlib.sha256(raw).hexdigest()
                adapted = block.replace("/usr/bin/python3 /opt/mithril-hermes-research/historical-research-context.py", "reader")
                adapted = adapted.replace("/var/lib/mithril-agent-research/historical/packet-backtest.json", shlex.quote(str(report)))
                script = '''set -eu
research_query=$1/prompt
reader() {
  printf called > "$research_query.called"
''' + ("  printf PRIVATE; printf PRIVATE >&2; return 1\n" if mode == "failed-reader" else
                       "  " + shlex.quote(sys.executable) + " " + shlex.quote(str(HERE / "historical-research-context.py")) + ' "$@"\n') + "}\n" + adapted
                env = dict(os.environ, PYTHONDONTWRITEBYTECODE="1", MITHRIL_HERMES_RETROSPECTIVE_REPORT_SHA256=digest,
                           MITHRIL_HERMES_RETROSPECTIVE_PACKET_SHA256="a" * 64)
                if mode == "wrong-pin": env["MITHRIL_HERMES_RETROSPECTIVE_REPORT_SHA256"] = "0" * 64
                if mode == "missing-report-pin": env.pop("MITHRIL_HERMES_RETROSPECTIVE_REPORT_SHA256")
                if mode == "missing-packet-pin": env.pop("MITHRIL_HERMES_RETROSPECTIVE_PACKET_SHA256")
                result = subprocess.run(["/bin/sh", "-c", script, "test", directory], env=env, capture_output=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.stderr, b"")
                prompt = (Path(directory) / "prompt").read_bytes()
                self.assertNotIn(b"PRIVATE", prompt)
                self.assertEqual((Path(directory) / "prompt.called").exists(), not mode.startswith("missing"))
                if mode == "success":
                    projected = context.project(report, digest, "packet-backtest", "a" * 64)
                    self.assertIn(context.canonical(projected), prompt)
                else:
                    self.assertIn(b"Retrospective context: unavailable", prompt)


if __name__ == "__main__":
    unittest.main()
