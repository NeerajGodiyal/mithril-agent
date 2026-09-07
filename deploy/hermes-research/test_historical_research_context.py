import copy
import hashlib
import importlib.util
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


SPEC = importlib.util.spec_from_file_location("historical_context", Path(__file__).with_name("historical-research-context.py"))
context = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(context)


def fixture():
    comparison = {
        "simulated_cash_change_usdt": -2.0, "simulated_account_return_percent": -0.02,
        "engine_trade_pnl_minus_cash_change_usdt": 0.01,
        "cash_flow_reconciled": True, "engine_trade_pnl_reconciled": False,
        "max_drawdown_percent": None, "completed_trades": 1, "filled_orders": 2,
        "engine_metrics": {"finishing_balance": 9998.0, "net_profit": -1.99, "fee": 0.2,
                           "total": 1, "total_open_trades": 0, "total_winning_trades": 0,
                           "total_losing_trades": 1, "max_drawdown": 0},
        "drawdown_unavailable_reason": "private-prose-do-not-copy",
    }
    report = {"experiment": "fixed_historical_pipeline", "jesse_commit": "44a0ed432fd74133edaf273f3b56100042f564b3",
              "script_sha256": "a" * 64, "reporting_revision_of_script_sha256": "b" * 64,
              "declaration": copy.deepcopy(context.DECLARATION),
              "config_sha256": hashlib.sha256(context.canonical(context.DECLARATION)).hexdigest(),
              "period_state_shared": False, "parameter_search": False,
              "profitability_qualification": False, "mithril_strategy": False,
              "holdout_status": context.HOLDOUT,
              "cost_model": "configured exchange fee only; no Jupiter execution costs",
              "unknown": "private-prose-do-not-copy", "experiments": []}
    for period, date in context.PERIODS:
        report["experiments"].append({"period": period, "warmup_rows": 30, "evaluation_rows": 1410,
            "source": {"date": date, "archive": f"SOLUSDT-1m-{date}.zip", "rows": 1440,
                       "imputed_rows": 0, "timestamp_unit": "milliseconds",
                       "archive_sha256": "c" * 64, "normalized_sha256": "d" * 64},
            "comparisons": [dict(copy.deepcopy(comparison), strategy=name) for name in ("BuyHold", "SMA1030")]})
    return report


class HistoricalContextTest(unittest.TestCase):
    def project(self, report=None, raw=None, digest=None):
        if raw is None:
            raw = context.canonical(report if report is not None else fixture())
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "private-report.json"
            path.write_bytes(raw)
            path.chmod(0o600)
            return context.project(path, digest or hashlib.sha256(raw).hexdigest())

    def test_negative_cash_and_prose_filter(self):
        output = self.project()
        self.assertEqual(output["experiments"][0]["comparisons"][0]["simulated_cash_change_usdt"], -2)
        self.assertFalse(output["financial_results_independently_verified"])
        self.assertFalse(output["recorded_basis_eligible"])
        self.assertNotIn(b"private-prose", context.canonical(output))
        self.assertNotIn("cash_flow_reconciled", output)

    def test_bad_digest_json_and_nonfinite(self):
        with self.assertRaises(ValueError):
            self.project(digest="0" * 64)
        for raw in (b'{"x":1,"X":2}', b'{"x":NaN}', b'{"x":1e999}', b'[]'):
            with self.subTest(raw=raw), self.assertRaises(ValueError):
                self.project(raw=raw)

    def test_shape_and_arithmetic_rejections(self):
        for case in ("missing-period", "duplicate-period", "missing-strategy", "duplicate-strategy",
                     "bool-number", "bool-count", "cash", "gap", "return", "config", "authority",
                     "failed-cash-proof", "false-reconciliation", "order-count"):
            with self.subTest(case=case):
                report = fixture()
                experiment = report["experiments"][0]
                row = experiment["comparisons"][0]
                if case == "missing-period": report["experiments"].pop()
                elif case == "duplicate-period": report["experiments"][1] = copy.deepcopy(experiment)
                elif case == "missing-strategy": experiment["comparisons"].pop()
                elif case == "duplicate-strategy": experiment["comparisons"][1] = copy.deepcopy(row)
                elif case == "bool-number": row["engine_metrics"]["fee"] = True
                elif case == "bool-count": row["completed_trades"] = True
                elif case == "cash": row["simulated_cash_change_usdt"] = -3
                elif case == "gap": row["engine_trade_pnl_minus_cash_change_usdt"] = 4
                elif case == "return": row["simulated_account_return_percent"] = 4
                elif case == "config": report["declaration"]["fast_mode"] = 0
                elif case == "authority": report["profitability_qualification"] = True
                elif case == "failed-cash-proof": row["cash_flow_reconciled"] = False
                elif case == "false-reconciliation": row["engine_trade_pnl_reconciled"] = True
                elif case == "order-count": row["filled_orders"] = 3
                with self.assertRaises(ValueError): self.project(report)

    def test_cli_errors_are_static(self):
        result = subprocess.run([sys.executable, str(Path(__file__).with_name("historical-research-context.py")),
                                 "--report", "/private/secret-sentinel", "--sha256", "a" * 64],
                                capture_output=True, env=dict(os.environ, PYTHONDONTWRITEBYTECODE="1"))
        self.assertEqual(result.returncode, 1)
        self.assertEqual(result.stdout, b"")
        self.assertEqual(result.stderr, b"historical research context unavailable\n")

    def test_actual_wrapper_block(self):
        wrapper = Path(__file__).with_name("run-market-scout.sh").read_text()
        start = wrapper.index("  historical_research=unavailable\n")
        end = wrapper.index('  if [ -n "$sol_outcome_history$jup_outcome_history" ]; then', start)
        block = wrapper[start:end].replace(
            "/usr/bin/python3 /opt/mithril-hermes-research/historical-research-context.py", "fake_reader")
        for mode in ("missing", "failure", "success"):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as directory:
                script = '''set -eu
research_query=$1/prompt
mode=$2
MITHRIL_HERMES_HISTORICAL_REPORT_SHA256=
[ "$mode" = missing ] || MITHRIL_HERMES_HISTORICAL_REPORT_SHA256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
fake_reader() {
  printf called > "$research_query.called"
  [ "$mode" != missing ] || exit 99
  if [ "$mode" = failure ]; then printf PRIVATE; printf PRIVATE >&2; return 1; fi
  printf '{"simulated_cash_change_usdt":-2}'
}
''' + block + '\ncat "$research_query"\n'
                result = subprocess.run(["/bin/sh", "-c", script, "test", directory, mode], capture_output=True)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.stderr, b"")
                self.assertNotIn(b"PRIVATE", result.stdout)
                self.assertEqual((Path(directory) / "prompt.called").exists(), mode != "missing")
                if mode == "success":
                    self.assertIn(b'"simulated_cash_change_usdt":-2', result.stdout)
                else:
                    self.assertIn(b"Historical context: unavailable", result.stdout)

    def test_wrapper_with_real_reader(self):
        wrapper = Path(__file__).with_name("run-market-scout.sh").read_text()
        start = wrapper.index("  historical_research=unavailable\n")
        end = wrapper.index('  if [ -n "$sol_outcome_history$jup_outcome_history" ]; then', start)
        block = wrapper[start:end].replace(
            "/usr/bin/python3 /opt/mithril-hermes-research/historical-research-context.py",
            '"$python" "$reader"',
        ).replace("/var/lib/mithril-agent-research/historical/report.json", '"$report"')
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "report.json"
            raw = context.canonical(fixture())
            path.write_bytes(raw)
            path.chmod(0o600)
            digest = hashlib.sha256(raw).hexdigest()
            expected = context.canonical(context.project(path, digest))
            script = 'set -eu\npython=$1\nreader=$2\nreport=$3\nresearch_query=$4\nMITHRIL_HERMES_HISTORICAL_REPORT_SHA256=$5\n' + block
            prompt = Path(directory) / "prompt.md"
            for pin in (digest, "0" * 64):
                with self.subTest(valid_pin=pin == digest):
                    prompt.write_bytes(b"")
                    result = subprocess.run([
                        "/bin/sh", "-c", script, "test", sys.executable,
                        str(Path(__file__).with_name("historical-research-context.py")),
                        str(path), str(prompt), pin,
                    ], capture_output=True)
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertEqual(result.stdout + result.stderr, b"")
                    rendered = prompt.read_bytes()
                    self.assertNotIn(b"private-prose", rendered)
                    if pin == digest:
                        self.assertIn(b"Historical context: " + expected, rendered)
                    else:
                        self.assertIn(b"Historical context: unavailable", rendered)
                        self.assertNotIn(expected, rendered)


if __name__ == "__main__":
    unittest.main()
