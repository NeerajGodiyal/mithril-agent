import copy
import importlib.util
import io
import json
import os
from pathlib import Path
import stat
import subprocess
import tempfile
import types
import unittest
from unittest.mock import patch


SPEC = importlib.util.spec_from_file_location("perps_scout", Path(__file__).with_name("run-perps-scout.py"))
scout = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(scout)


class PerpsScoutTest(unittest.TestCase):
    def test_low_disk_stops_before_context_or_model_work(self):
        with tempfile.TemporaryDirectory() as root, \
                patch.object(scout, "ROOT", Path(root)), patch.object(scout, "RUNTIME", Path(root)), \
                patch.object(scout.os, "geteuid", return_value=0), \
                patch.object(scout.pwd, "getpwnam"), \
                patch.object(Path, "lstat", return_value=types.SimpleNamespace(st_uid=0, st_mode=stat.S_IFDIR | 0o711)), \
                patch.object(scout.shutil, "disk_usage", return_value=types.SimpleNamespace(free=(1 << 30) - 1)), \
                patch.object(scout, "run_symbol") as symbol:
            with self.assertRaisesRegex(ValueError, "free space"):
                scout.run()
            symbol.assert_not_called()

    def context(self):
        return json.dumps({
            "status": "advisory_context", "symbol": "SOL", "paper_only": True,
            "authorized": False, "promotable": False, "content_sha256": "a" * 64,
            "training": [{"tape_sha256": "b" * 64}], "resolved_outcomes": [],
        }).encode() + b"\n"

    def session(self):
        _, hypothesis, prompt = scout.make_prompt(self.context(), "SOL")
        return {
            "id": "isolated", "source": "cli", "parent_session_id": None,
            "started_at": 101, "ended_at": 104, "end_reason": "agent_close",
            "messages": [
                {"role": "user", "content": prompt, "timestamp": 101},
                {"role": "assistant", "finish_reason": "stop", "timestamp": 104,
                 "content": json.dumps({"hypothesis_id": hypothesis, "symbol": "SOL",
                     "risk_arm": "conservative", "strategy": "regime",
                     "rationale": "Test the existing baseline; the short sample is uncertain."})},
            ],
        }

    def extract(self, session):
        raw = json.dumps(session).encode() + b"\n"
        _, _, prompt = scout.make_prompt(self.context(), "SOL")
        return scout.extract_bound_proposal(raw, prompt, self.context(), "SOL", 100, 105)

    def test_exact_context_and_session_binding(self):
        proposal, receipt = self.extract(self.session())
        self.assertEqual(receipt["context_sha256"], "a" * 64)
        self.assertEqual(receipt["proposal_input_sha256"], scout.sha256(proposal))
        self.assertEqual(receipt["tool_calls"], 0)
        self.assertFalse(receipt["authorized"])
        for mutation in ("prompt", "symbol", "identity", "tools", "pending", "future", "extra"):
            session = self.session()
            final = session["messages"][-1]
            if mutation == "prompt":
                session["messages"][0]["content"] += " another context"
            elif mutation in ("symbol", "identity", "extra"):
                value = json.loads(final["content"])
                value[{"symbol": "symbol", "identity": "hypothesis_id", "extra": "path"}[mutation]] = "changed"
                final["content"] = json.dumps(value)
            elif mutation == "tools":
                final["tool_calls"] = [{"function": {"name": "web_search"}, "id": "unexpected"}]
            elif mutation == "pending":
                session["end_reason"] = "interrupted"
            else:
                session["ended_at"] = 106
            with self.subTest(mutation=mutation), self.assertRaises(ValueError):
                self.extract(session)

    def test_empty_export_and_unrelated_sibling_are_rejected(self):
        _, _, prompt = scout.make_prompt(self.context(), "SOL")
        session = self.session()
        sibling = copy.deepcopy(session)
        sibling.update(id="sibling", parent_session_id="isolated", source="subagent")
        for raw in (b"", (json.dumps(session) + "\n" + json.dumps(sibling)).encode()):
            with self.assertRaises(ValueError):
                scout.extract_bound_proposal(raw, prompt, self.context(), "SOL", 100, 105)

    def test_model_failure_never_calls_freeze(self):
        with tempfile.TemporaryDirectory() as root, patch.object(scout.os, "chown"), \
                patch.object(scout, "as_research", return_value=self.context()) as host, \
                patch.object(scout, "container", side_effect=subprocess.TimeoutExpired("model", 150)):
            directory = Path(root) / "archive"
            directory.mkdir()
            progress = {}
            with self.assertRaises(subprocess.TimeoutExpired):
                scout.run_symbol("SOL", directory, Path(root) / "home",
                    types.SimpleNamespace(pw_uid=os.getuid(), pw_gid=os.getgid()), "test", progress)
            self.assertEqual(host.call_count, 1)
            self.assertNotIn("perps-freeze", host.call_args.args)
            self.assertEqual(progress["phase"], "model_proposal")

    def test_freeze_uses_only_original_context_tape_paths(self):
        frozen = json.dumps({"context_sha256": "a" * 64, "status": "pending_advisory",
            "authorized": False, "promotable": False, "content_sha256": "c" * 64,
            "input": {"hypothesis_id": "hermes-" + "a" * 48, "strategy": "regime", "risk_arm": "conservative", "rationale": "private model prose"},
            "target_episode": "9", "frozen_at": "2026-09-05T20:00:00Z"}).encode()
        with tempfile.TemporaryDirectory() as root, patch.object(scout.os, "chown"), \
                patch.object(scout, "container"), \
                patch.object(scout, "as_research", side_effect=[self.context(), b"{}", frozen]) as host:
            directory = Path(root) / "archive"
            directory.mkdir(mode=0o700)
            result = scout.run_symbol("SOL", directory, Path(root) / "home",
                types.SimpleNamespace(pw_uid=os.getuid(), pw_gid=os.getgid()), "test", {})
            self.assertEqual(result["target_episode"], "9")
            self.assertEqual((result["strategy"], result["risk_arm"], result["training_tapes"], result["resolved_outcomes"]),
                             ("regime", "conservative", 1, 0))
            self.assertNotIn("rationale", result)
            freeze_args = host.call_args.args
            self.assertIn("perps-freeze", freeze_args)
            self.assertIn(str(directory / "evidence/context.json"), freeze_args)
            self.assertIn(str(scout.STATE.parent / "tapes/sol" / ("b" * 64 + ".json")), freeze_args)
            self.assertTrue((directory / "invocation.json").is_file())

    def test_dashboard_projection_is_private_and_excludes_prose_and_paths(self):
        status = {"version": 1, "paper_only": True, "authorized": False, "promotable": False,
                  "run_id": "d" * 32, "finished_at": "2026-09-05T20:00:00Z",
                  "prompt": "private", "markets": [{"symbol": "SOL", "status": "pending_advisory",
                  "target_episode": "9", "context_sha256": "a" * 64, "proposal_sha256": "b" * 64,
                  "strategy": "regime", "risk_arm": "conservative", "training_tapes": 1,
                  "resolved_outcomes": 0, "rationale": "private", "path": "/private/session"}]}
        with tempfile.TemporaryDirectory() as root, \
                patch.object(scout, "DASHBOARD", Path(root).resolve() / "perps-proposals.json"), \
                patch.object(scout.pwd, "getpwnam", return_value=types.SimpleNamespace(pw_uid=os.getuid(), pw_gid=os.getgid())):
            scout.publish_dashboard(status)
            raw = scout.DASHBOARD.read_bytes()
            self.assertNotIn(b"private", raw)
            self.assertNotIn(b"prompt", raw)
            self.assertEqual(json.loads(raw)["markets"][0]["resolved_outcomes"], 0)
            self.assertEqual(stat.S_IMODE(scout.DASHBOARD.stat().st_mode), 0o600)
            self.assertEqual(scout.DASHBOARD.stat().st_uid, os.getuid())
            before = raw
            with patch.object(scout.os, "replace", side_effect=OSError("failure")):
                with self.assertRaises(OSError):
                    scout.publish_dashboard(status)
            self.assertEqual(scout.DASHBOARD.read_bytes(), before)
            self.assertEqual(list(Path(root).resolve().iterdir()), [scout.DASHBOARD])
            scout.DASHBOARD.unlink()
            target = Path(root) / "do-not-touch"
            target.write_text("retained")
            scout.DASHBOARD.symlink_to(target)
            with self.assertRaisesRegex(ValueError, "destination"):
                scout.publish_dashboard(status)
            self.assertEqual(target.read_text(), "retained")
            scout.DASHBOARD.unlink()
            Path(root).chmod(0o722)
            with self.assertRaisesRegex(ValueError, "directory"):
                scout.publish_dashboard(status)
            self.assertFalse(scout.DASHBOARD.exists())

    def test_publication_failure_preserves_private_success_receipt(self):
        row = {"symbol": "SOL", "status": "pending_advisory", "proposal_sha256": "a" * 64}
        with tempfile.TemporaryDirectory() as root, \
                patch.object(scout, "ROOT", Path(root)), patch.object(scout, "RUNTIME", Path(root)), \
                patch.object(scout, "SYMBOLS", ("SOL",)), patch.object(scout.os, "geteuid", return_value=0), \
                patch.object(scout.pwd, "getpwnam"), \
                patch.object(Path, "lstat", return_value=types.SimpleNamespace(st_uid=0, st_mode=stat.S_IFDIR | 0o711)), \
                patch.object(scout.shutil, "disk_usage", return_value=types.SimpleNamespace(free=2 << 30)), \
                patch.object(scout, "run_symbol", return_value=row), \
                patch.object(scout, "publish_dashboard", side_effect=OSError("private failure detail")), \
                patch.object(scout.sys, "stdout", new_callable=io.StringIO) as output, \
                patch.object(scout.sys, "stderr", new_callable=io.StringIO) as error:
            with self.assertRaises(OSError):
                scout.run()
            retained = json.loads((Path(root) / "latest.json").read_bytes())
            self.assertEqual(retained["markets"], [row])
            self.assertEqual(json.loads(output.getvalue())["markets"], [row])
            self.assertIn("private receipt retained", error.getvalue())
            self.assertNotIn("private failure detail", error.getvalue())

    def invocation(self, root, number=1, symbol="SOL"):
        directory = Path(root) / f"{number:032x}" / symbol.lower()
        directory.mkdir(parents=True)
        receipt = {"version": 1, "status": "pending_advisory", "symbol": symbol,
                   "paper_only": True, "authorized": False, "promotable": False,
                   "context_sha256": f"{number % 256:02x}" * 32, "proposal_sha256": f"{number + 100:064x}",
                   "target_episode": str(number), "run_started": number * 10,
                   "run_finished": number * 10 + 1, "frozen_at": "2026-09-05T20:00:00Z"}
        scout.create_invocation_receipt(directory / "invocation.json", receipt)
        return directory, receipt

    def outcome(self, receipt, status="evaluated"):
        return {"version": 1, "status": status, "paper_only": True, "authorized": False,
                "promotable": False, "proposal_sha256": receipt["proposal_sha256"],
                "target_episode": receipt["target_episode"], "content_sha256": "e" * 64,
                "observed_at": "2026-09-05T21:00:00Z", "start_sha256": "b" * 64,
                "terminal_sha256": "c" * 64,
                "proposed": {"strategy": "regime", "risk_arm": "conservative"}}

    def selection(self, receipt, outcome, status="qualified_paper_plan_selected"):
        selected = {"status": status, "symbol": receipt["symbol"], "paper_only": True,
                    "authorized": False, "promotable": False, "execution_enabled": False,
                    "strategy": "regime", "risk_arm": "conservative",
                    "pointer_updated": status == "qualified_paper_plan_selected",
                    "rollback_updated": status == "qualified_paper_plan_selected",
                    "evaluated_proposal": {"version": 1,
                    "proposal_sha256": receipt["proposal_sha256"], "context_sha256": receipt["context_sha256"],
                    "evaluation_sha256": outcome["content_sha256"], "target_episode": receipt["target_episode"],
                    "frozen_at": receipt["frozen_at"], "observed_at": outcome["observed_at"],
                    "start_sha256": outcome["start_sha256"], "terminal_sha256": outcome["terminal_sha256"]}}
        if status != "evaluated_proposal_not_selected":
            selected["plan_sha256"] = "f" * 64
        return selected

    def test_reconciliation_retains_pending_and_processes_creation_order_not_latest(self):
        with tempfile.TemporaryDirectory() as root, patch.object(scout, "ROOT", Path(root)), \
                patch.object(scout, "SYMBOLS", ("SOL",)):
            newer, second = self.invocation(root, 2)
            older, first = self.invocation(root, 1)
            (Path(root) / "latest.json").write_text('{"unrelated":"newer dashboard row"}')
            with patch.object(scout, "as_research", return_value=json.dumps(self.outcome(first, "pending")).encode()) as host:
                scout.reconcile_proposals(True)
                scout.reconcile_proposals(True)
                self.assertEqual(host.call_count, 2)
                self.assertTrue(all("perps-evaluate" in call.args for call in host.call_args_list))
                self.assertEqual(host.call_args.args[-1], scout.STATE.parent / "proposals/sol" / ("hermes-" + first["context_sha256"][:48] + ".json"))
                self.assertFalse((older / "selection-attempt.json").exists())
                self.assertFalse((newer / "selection-attempt.json").exists())

    def test_selection_disabled_and_terminal_unevaluable_never_select(self):
        for enabled, status in ((False, "evaluated"), (True, "unevaluable")):
            with self.subTest(enabled=enabled), tempfile.TemporaryDirectory() as root, \
                    patch.object(scout, "ROOT", Path(root)), patch.object(scout, "SYMBOLS", ("SOL",)):
                directory, receipt = self.invocation(root)
                with patch.object(scout, "as_research", return_value=json.dumps(self.outcome(receipt, status)).encode()) as host:
                    scout.reconcile_proposals(enabled)
                    self.assertEqual(host.call_count, 1 if enabled else 0)
                    self.assertFalse((directory / "selection-attempt.json").exists())

    def test_selection_intent_precedes_call_and_completed_statuses_never_retry(self):
        statuses = ("qualified_paper_plan_selected", "qualified_paper_plan_already_selected",
                    "qualified_paper_plan_retired", "evaluated_proposal_not_selected")
        for status in statuses:
            with self.subTest(status=status), tempfile.TemporaryDirectory() as root, \
                    patch.object(scout, "ROOT", Path(root)), patch.object(scout, "SYMBOLS", ("SOL",)):
                directory, receipt = self.invocation(root)
                outcome = self.outcome(receipt)
                def host(*args, **kwargs):
                    if "perps-evaluate" in args:
                        return json.dumps(outcome).encode()
                    intent = scout.private_invocation(directory / "selection-attempt.json")
                    self.assertEqual(intent["evaluation_sha256"], outcome["content_sha256"])
                    selected = self.selection(receipt, outcome, status)
                    selected["comparison"] = {"private_extra": "must not project"}
                    return json.dumps(selected).encode()
                with patch.object(scout, "as_research", side_effect=host) as execute:
                    scout.reconcile_proposals(True)
                    scout.reconcile_proposals(True)
                    self.assertEqual(execute.call_count, 2)
                result = scout.private_invocation(directory / "selection-result.json")
                self.assertEqual(result["status"], status)
                self.assertNotIn("comparison", result)

    def test_ambiguous_selection_or_result_write_failure_never_retries(self):
        for failure in ("selector", "receipt"):
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as root, \
                    patch.object(scout, "ROOT", Path(root)), patch.object(scout, "SYMBOLS", ("SOL",)), \
                    patch.object(scout.sys, "stderr", new_callable=io.StringIO):
                directory, receipt = self.invocation(root)
                outcome = self.outcome(receipt)
                selected = self.selection(receipt, outcome)
                original = scout.create_invocation_receipt
                def write(path, value):
                    if failure == "receipt" and path.name == "selection-result.json":
                        raise OSError("after pointer update")
                    original(path, value)
                outputs = [json.dumps(outcome).encode(), subprocess.TimeoutExpired("selector", 30)
                           if failure == "selector" else json.dumps(selected).encode()]
                with patch.object(scout, "as_research", side_effect=outputs) as host, \
                        patch.object(scout, "create_invocation_receipt", side_effect=write):
                    scout.reconcile_proposals(True)
                    scout.reconcile_proposals(True)
                    self.assertEqual(host.call_count, 2)
                self.assertTrue((directory / "selection-attempt.json").exists())
                self.assertFalse((directory / "selection-result.json").exists())

    def test_reconciliation_rejects_identity_symlinks_and_flag(self):
        for mutation in ("symbol", "hypothesis", "symlink", "permissions"):
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as root, \
                    patch.object(scout, "ROOT", Path(root)), patch.object(scout, "SYMBOLS", ("SOL",)), \
                    patch.object(scout, "as_research") as host:
                directory, receipt = self.invocation(root)
                path = directory / "invocation.json"
                if mutation == "symlink":
                    path.rename(directory / "original.json")
                    path.symlink_to(directory / "original.json")
                elif mutation == "permissions":
                    path.chmod(0o644)
                else:
                    receipt["symbol" if mutation == "symbol" else "hypothesis_id"] = "changed"
                    path.write_text(json.dumps(receipt))
                with self.assertRaises(ValueError):
                    scout.reconcile_proposals(True)
                host.assert_not_called()
        with patch.dict(scout.os.environ, {"MITHRIL_HERMES_PERPS_SELECT": "true"}), \
                patch.object(scout.os, "geteuid", return_value=0), patch.object(scout, "reconcile_proposals") as reconcile:
            with self.assertRaisesRegex(ValueError, "flag"):
                scout.run()
            reconcile.assert_not_called()

    def test_evaluation_failure_does_not_block_other_markets_or_select_wrong_identity(self):
        with tempfile.TemporaryDirectory() as root, patch.object(scout, "ROOT", Path(root)), \
                patch.object(scout, "SYMBOLS", ("SOL", "BTC")), \
                patch.object(scout.sys, "stderr", new_callable=io.StringIO):
            sol_dir, sol = self.invocation(root, 1, "SOL")
            btc_dir, btc = self.invocation(root, 2, "BTC")
            wrong = self.outcome(sol)
            wrong["proposal_sha256"] = "f" * 64
            with patch.object(scout, "as_research", side_effect=[json.dumps(wrong).encode(),
                    json.dumps(self.outcome(btc, "pending")).encode()]) as host:
                scout.reconcile_proposals(True)
                self.assertEqual(host.call_count, 2)
                self.assertTrue(all("perps-evaluate" in call.args for call in host.call_args_list))
            self.assertFalse((sol_dir / "selection-attempt.json").exists())
            self.assertFalse((btc_dir / "selection-attempt.json").exists())

    def test_reconciliation_bound_and_mismatched_selector_leave_no_completed_claim(self):
        with tempfile.TemporaryDirectory() as root, patch.object(scout, "ROOT", Path(root)), \
                patch.object(scout, "SYMBOLS", ("SOL",)), patch.object(scout, "as_research") as host:
            for number in range(1, 258):
                self.invocation(root, number)
            with self.assertRaisesRegex(ValueError, "bound"):
                scout.reconcile_proposals(True)
            host.assert_not_called()
        with tempfile.TemporaryDirectory() as root, patch.object(scout, "ROOT", Path(root)), \
                patch.object(scout, "SYMBOLS", ("SOL",)), \
                patch.object(scout.sys, "stderr", new_callable=io.StringIO):
            directory, receipt = self.invocation(root)
            outcome = self.outcome(receipt)
            selected = self.selection(receipt, outcome)
            selected["evaluated_proposal"]["context_sha256"] = "f" * 64
            with patch.object(scout, "as_research", side_effect=[json.dumps(outcome).encode(), json.dumps(selected).encode()]) as host:
                scout.reconcile_proposals(True)
                scout.reconcile_proposals(True)
                self.assertEqual(host.call_count, 2)
            self.assertTrue((directory / "selection-attempt.json").exists())
            self.assertFalse((directory / "selection-result.json").exists())

    def test_container_cleanup_must_be_confirmed(self):
        for remaining, error in ((b"", None), (b"leftover", scout.ContainerCleanupError)):
            with self.subTest(remaining=remaining), patch.object(scout.subprocess, "run", side_effect=[
                subprocess.CompletedProcess([], 0), subprocess.CompletedProcess([], 1),
                subprocess.CompletedProcess([], 0, stdout=remaining),
            ]):
                if error:
                    with self.assertRaises(error):
                        scout.container({}, "owned-container", timeout=1)
                else:
                    scout.container({}, "owned-container", timeout=1)

    def test_service_stop_cleanup_selects_only_owned_label(self):
        with patch.object(scout.os, "geteuid", return_value=0), patch.object(scout.subprocess, "run", side_effect=[
            subprocess.CompletedProcess([], 0, stdout=b"abc123\n"),
            subprocess.CompletedProcess([], 0), subprocess.CompletedProcess([], 0, stdout=b""),
        ]) as execute:
            scout.cleanup_containers()
            self.assertEqual(execute.call_args_list[0].args[0][-1], "label=" + scout.CONTAINER_LABEL)
            self.assertEqual(execute.call_args_list[1].args[0], ["/usr/bin/docker", "rm", "--force", "abc123"])

    def test_host_interruption_kills_and_reaps_entire_process_group(self):
        with patch.object(scout.subprocess, "Popen") as popen, patch.object(scout.os, "killpg") as kill:
            process = popen.return_value.__enter__.return_value
            process.pid = 123
            process.communicate.side_effect = [scout.RunInterrupted(), (b"", None)]
            with self.assertRaises(scout.RunInterrupted):
                scout.as_research("test-command")
            kill.assert_called_once_with(123, scout.signal.SIGKILL)
            self.assertEqual(process.communicate.call_count, 2)
            self.assertTrue(popen.call_args.kwargs["start_new_session"])


if __name__ == "__main__":
    unittest.main()
