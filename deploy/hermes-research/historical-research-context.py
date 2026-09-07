"""Project one operator-pinned historical report; never grant evidence authority."""

import argparse
import datetime
import hashlib
import importlib.util
import json
import math
import re
from pathlib import Path
import sys


SPEC = importlib.util.spec_from_file_location("research_evidence", Path(__file__).with_name("build-research-evidence.py"))
evidence = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(evidence)

PERIODS = [["training", "2024-01-01"], ["validation", "2024-01-02"], ["test", "2024-01-03"]]
DECLARATION = {
    "config": {"starting_balance": 10000, "fee": 0.001, "type": "spot", "exchange": "Binance Spot", "warm_up_candles": 30},
    "periods": PERIODS, "warmup_rows": 30, "entry_notional_usdt": 100.0,
    "sma_windows": [10, 30], "candidate_rule": "long while closed-candle SMA10 exceeds SMA30",
    "final_close_index": 1408, "fast_mode": False,
}
HOLDOUT = "previously evaluated; reporting-only rerun, not a fresh test"


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"), allow_nan=False).encode()


def require(condition):
    if not condition:
        raise ValueError("historical report is invalid")


def number(value):
    require(type(value) in (int, float) and math.isfinite(value))
    return value


def count(value):
    require(type(value) is int and 0 <= value <= 10000)
    return value


def hash_value(value):
    require(isinstance(value, str) and evidence.SHA256.fullmatch(value) is not None)
    return value


def project(path, expected_sha256, kind="historical", packet_sha256=None):
    hash_value(expected_sha256)
    raw = evidence.read_private(Path(path), 128 * 1024)
    require(hashlib.sha256(raw).hexdigest() == expected_sha256)
    report = evidence.strict_json_object(raw)
    if kind == "packet-backtest":
        return project_packet_backtest(report, expected_sha256, packet_sha256)
    require(kind == "historical" and packet_sha256 is None)
    require(report["experiment"] == "fixed_historical_pipeline")
    require(report["jesse_commit"] == "44a0ed432fd74133edaf273f3b56100042f564b3")
    for field in ("period_state_shared", "parameter_search", "profitability_qualification", "mithril_strategy"):
        require(report[field] is False)
    require(report["holdout_status"] == HOLDOUT)
    require(report["cost_model"] == "configured exchange fee only; no Jupiter execution costs")
    # Canonical bytes distinguish booleans from numeric configuration values.
    require(canonical(report["declaration"]) == canonical(DECLARATION))
    require(hash_value(report["config_sha256"]) == hashlib.sha256(canonical(DECLARATION)).hexdigest())
    experiments = report["experiments"]
    require(type(experiments) is list and len(experiments) == 3)
    rows = []
    for experiment, (period, date) in zip(experiments, PERIODS):
        require(experiment["period"] == period)
        require(count(experiment["warmup_rows"]) == 30 and count(experiment["evaluation_rows"]) == 1410)
        source = experiment["source"]
        require(source["date"] == date and source["archive"] == f"SOLUSDT-1m-{date}.zip")
        require(count(source["rows"]) == 1440 and count(source["imputed_rows"]) == 0)
        require(source["timestamp_unit"] == "milliseconds")
        comparisons = experiment["comparisons"]
        require(type(comparisons) is list and len(comparisons) == 2)
        projected = []
        for result, strategy in zip(comparisons, ("BuyHold", "SMA1030")):
            require(result["strategy"] == strategy and result["max_drawdown_percent"] is None)
            require(result["cash_flow_reconciled"] is True and type(result["engine_trade_pnl_reconciled"]) is bool)
            metrics = result["engine_metrics"]
            cash = number(result["simulated_cash_change_usdt"])
            ending = number(metrics["finishing_balance"])
            pnl = number(metrics["net_profit"])
            gap = number(result["engine_trade_pnl_minus_cash_change_usdt"])
            account_return = number(result["simulated_account_return_percent"])
            fee = number(metrics["fee"])
            require(ending >= 0 and fee >= 0)
            require(math.isclose(cash, ending - 10000, rel_tol=1e-9, abs_tol=1e-8))
            require(math.isclose(gap, pnl - cash, rel_tol=1e-9, abs_tol=1e-8))
            require(math.isclose(account_return, cash / 10000 * 100, rel_tol=1e-9, abs_tol=1e-8))
            require(result["engine_trade_pnl_reconciled"] is math.isclose(pnl, cash, rel_tol=1e-9, abs_tol=1e-8))
            trades = count(result["completed_trades"])
            require(count(result["filled_orders"]) == 2 * trades)
            require(count(metrics["total"]) == trades and count(metrics["total_open_trades"]) == 0)
            wins, losses = count(metrics["total_winning_trades"]), count(metrics["total_losing_trades"])
            require(wins + losses <= trades)
            projected.append({"strategy": strategy, "simulated_cash_change_usdt": cash,
                              "simulated_account_return_percent": account_return,
                              "ending_simulated_cash_usdt": ending, "engine_trade_pnl_usdt": pnl,
                              "engine_trade_pnl_minus_cash_change_usdt": gap, "engine_reported_fees_usdt": fee,
                              "completed_trades": trades, "filled_orders": count(result["filled_orders"]),
                              "max_drawdown_percent": None})
        rows.append({"period": period, "date": date, "warmup_rows": 30, "evaluation_rows": 1410,
                     "archive_sha256": hash_value(source["archive_sha256"]),
                     "normalized_sha256": hash_value(source["normalized_sha256"]), "comparisons": projected})
    return {"version": 1, "status": "historical_advisory_context", "report_sha256": expected_sha256,
            "script_sha256": hash_value(report["script_sha256"]), "config_sha256": report["config_sha256"],
            "reporting_revision_of_script_sha256": hash_value(report["reporting_revision_of_script_sha256"]),
            "jesse_commit": report["jesse_commit"], "market": "Binance Spot SOL-USDT",
            "artifact_identity_verified": True, "financial_results_independently_verified": False,
            "authorized": False, "promotable": False, "recorded_basis_eligible": False,
            "holdout_status": HOLDOUT, "starting_simulated_cash_usdt": 10000, "entry_notional_usdt": 100,
            "configured_fee_rate": 0.001,
            "limitations": "Historical CEX simulation only; producer cashflow assertions are not independently verified. No intraday drawdown estimate, current news, Jupiter fill equivalence, profitability qualification or trade permission.",
            "experiments": rows}


def integer(value, minimum=0, maximum=2**64 - 1):
    require(type(value) is int and minimum <= value <= maximum)
    return value


def money(value, signed=False):
    require(isinstance(value, str) and re.fullmatch(r"0|-?[1-9][0-9]*", value) is not None)
    integer(int(value), -(2**63) if signed else 0, 2**63 - 1 if signed else 2**64 - 1)
    return value


def project_packet_backtest(report, report_sha256, packet_sha256):
    require(integer(report["version"]) == 1 and report["kind"] == "retrospective_training")
    for key, expected in (("paper_only", True), ("pool_modelled", True), ("authorized", False), ("promotable", False)):
        require(report[key] is expected)
    require(hash_value(report["packet_sha256"]) == hash_value(packet_sha256))
    require(report["market"] in ("SOL/USDC", "JUP/USDC"))
    journal = report["journal"]
    day = datetime.date.fromisoformat(journal["day"])
    require(day.isoformat() == journal["day"])
    created_text = report["hypothesis_created_at"]
    require(isinstance(created_text, str) and re.fullmatch(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z", created_text) is not None)
    created = datetime.datetime.fromisoformat(created_text.replace("Z", "+00:00"))
    require(day == created.date() - datetime.timedelta(days=1) and created <= datetime.datetime.now(datetime.timezone.utc))
    records = integer(journal["records"], 1)
    limits = {"fast_window": (2, 120), "slow_window": (3, 240),
              "minimum_signal_bps": (1, 5000), "cooldown_seconds": (0, 86400)}
    changes, seen = [], set()
    require(type(report["parameter_changes"]) is list and 1 <= len(report["parameter_changes"]) <= len(limits))
    for change in report["parameter_changes"]:
        name = change["name"]
        require(isinstance(name, str) and name in limits and name not in seen)
        seen.add(name)
        current = integer(change["current"])
        proposed = integer(change["proposed"], *limits[name])
        require(current != proposed)
        changes.append({"name": name, "current": current, "proposed": proposed})
    lanes = {}
    for name in ("base", "candidate"):
        lane = report[name]
        counts = {key: integer(lane["counts"][key]) for key in
                  ("ticks", "sells", "buys", "refused", "filtered", "missed", "sell_signals", "buy_signals")}
        pending = lane["counts"].get("pending", False)
        require(type(pending) is bool)
        counts["pending"] = pending
        signals = counts["sell_signals"] + counts["buy_signals"]
        require(signals <= counts["ticks"] <= records)
        require(counts["sells"] <= counts["sell_signals"] and counts["buys"] <= counts["buy_signals"])
        require(all(counts[key] <= signals for key in ("refused", "filtered", "missed")))
        reasons = lane["filtered_reasons"]
        # Go emits null for a nil, empty diagnostic map.
        if reasons is None:
            reasons = {}
        require(type(reasons) is dict and set(reasons) <= {
            "slippage_mismatch", "quote_impact_limit", "trade_cost_floor_unavailable", "signal_below_cost_hurdle"})
        reasons = {key: integer(value, 1) for key, value in reasons.items()}
        require(sum(reasons.values()) == counts["filtered"])
        lanes[name] = {"counts": counts, "filtered_reasons": reasons,
                       "equity_micros": money(lane["equity_micros"]),
                       "versus_hold_micros": money(lane["versus_hold_micros"], signed=True),
                       "max_drawdown_micros": money(lane["max_drawdown_micros"])}
    require(lanes["base"]["counts"]["ticks"] == lanes["candidate"]["counts"]["ticks"])
    return {"version": 1, "status": "retrospective_training_context", "report_sha256": report_sha256,
            "packet_sha256": packet_sha256, "market": report["market"],
            "hypothesis_created_at": created_text, "parameter_changes": changes,
            "base_policy_sha256": hash_value(report["base_policy_sha256"]),
            "candidate_policy_sha256": hash_value(report["candidate_policy_sha256"]),
            "recorded_basis_sha256": hash_value(report["recorded_basis_sha256"]),
            "journal": {"day": journal["day"], "records": records, "chain_head_sha256": hash_value(journal["chain_head_sha256"])},
            "spread_bps": integer(report["spread_bps"], 1, 9999), "pool_modelled": True,
            "paper_only": True, "artifact_identity_verified": True,
            "financial_results_independently_verified": False, "recorded_basis_eligible": False,
            "authorized": False, "promotable": False, **lanes,
            "limitations": "Consumed-day training comparison, not untouched validation, current settings or executable costs. Money is simulated USD millionths. No profitability qualification or trade permission; do not lower assumed costs just to make a candidate pass."}


class Arguments(argparse.ArgumentParser):
    def error(self, message):
        raise ValueError("invalid arguments")


def main():
    parser = Arguments()
    parser.add_argument("--report", required=True)
    parser.add_argument("--sha256", required=True)
    parser.add_argument("--kind", choices=("historical", "packet-backtest"), default="historical")
    parser.add_argument("--packet-sha256")
    try:
        args = parser.parse_args()
        result = project(args.report, args.sha256, args.kind, args.packet_sha256)
        print(canonical(result).decode())
    except (OSError, ValueError, KeyError, TypeError, OverflowError, RecursionError):
        print("historical research context unavailable", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
