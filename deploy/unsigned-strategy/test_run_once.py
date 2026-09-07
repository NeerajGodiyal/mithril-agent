import fcntl
import importlib.util
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch


SPEC = importlib.util.spec_from_file_location("unsigned_runner", Path(__file__).with_name("run-once.py"))
runner = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(runner)


class RunnerTest(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name).resolve()
        self.state = self.root / "state"
        self.state.mkdir(mode=0o700)
        self.args = ["--strategy", str(self.root / "strategy.jsonl"),
                     "--inventory", str(self.root / "inventory.jsonl"),
                     "--authority-policy", str(self.root / "authority.json"),
                     "--submitter-policy", str(self.root / "recovery.json")]
        self.binary = self.root / "fake-agent"

    def fake(self, body):
        self.binary.write_text("#!" + sys.executable + "\n" + body)
        self.binary.chmod(0o700)

    def response(self, **fields):
        return {"status": "strategy_step_not_authorized", "current_head_sha256": "a" * 64,
                "strategy_pending": False, "can_sign": False, "can_submit": False, **fields}

    def status(self):
        return json.loads((self.state / "status.json").read_text())

    def execute(self, timeout=5):
        return runner.run_once(str(self.state), self.args, timeout, str(self.binary))

    def test_known_states_and_redaction(self):
        cases = [
            (self.response(blocked_reason="claim_not_reserved"), "blocked"),
            (self.response(observation_decision={"ReadyForQuote": False}), "observed_no_opportunity"),
            ({"status": "unsigned_claim_not_authorized", "head_sha256": "b" * 64,
              "pending": True, "recovered": False, "can_sign": False, "can_submit": False}, "awaiting_finality"),
        ]
        for result, state in cases:
            with self.subTest(state=state):
                result.update(claim_path="private-secret", action_id="private-secret")
                self.fake("print(" + repr(json.dumps(result)) + ")\n")
                self.assertEqual(self.execute(), 0)
                status = self.status()
                self.assertEqual(status["state"], state)
                self.assertIs(status["can_sign"], False)
                self.assertIs(status["can_submit"], False)
                self.assertNotIn("private-secret", json.dumps(status))
                self.assertEqual((self.state / "status.json").stat().st_mode & 0o777, 0o600)
                self.assertIn("finished_at", status)

    def test_errors_replace_old_success_without_leaking_output(self):
        bodies = ["import sys; sys.stderr.write('private-secret'); sys.exit(1)",
                  "print('private-secret')", "print('x' * 65537)",
                  "print(" + repr(json.dumps(self.response(can_sign=True))) + ")",
                  "print(" + repr(json.dumps(self.response())) + ")"]
        for body in bodies:
            with self.subTest(body=body[:25]):
                (self.state / "status.json").write_text('{"state":"old_success"}')
                self.fake(body)
                self.assertEqual(self.execute(), 1)
                self.assertEqual(self.status()["state"], "failed")
                self.assertNotIn("private-secret", json.dumps(self.status()))

    def test_only_step_arguments(self):
        for extra in (["--operation", "reserve"], ["--strategy", "/other"],
                      ["--historical-submitter-policy", "relative"], ["--help"],
                      ["--max-decision-age-seconds", "-1"]):
            with self.subTest(extra=extra), self.assertRaises(ValueError):
                runner.checked_args(self.args + extra)
        self.assertFalse((self.state / "status.json").exists())

    def test_permissions_and_symlinks_rejected(self):
        self.state.chmod(0o750)
        with self.assertRaises(ValueError):
            self.execute()
        self.state.chmod(0o700)
        alias = self.root / "alias"
        alias.symlink_to(self.state)
        with self.assertRaises(ValueError):
            runner.run_once(str(alias), self.args, binary=str(self.binary))
        lock = Path(self.args[3] + ".unsigned-runner.lock")
        victim = self.root / "victim"
        victim.write_text("original")
        lock.symlink_to(victim)
        with self.assertRaises(OSError):
            self.execute()
        self.assertEqual(victim.read_text(), "original")

    def test_both_locks_preserve_active_status(self):
        for target in (Path(self.args[3] + ".unsigned-runner.lock"), self.state / ".runner.lock"):
            with self.subTest(target=target.name):
                fd = os.open(target, os.O_CREAT | os.O_RDWR, 0o600)
                with os.fdopen(fd, "rb") as lock:
                    fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
                    (self.state / "status.json").write_text("original")
                    self.assertEqual(self.execute(), 75)
                    self.assertEqual((self.state / "status.json").read_text(), "original")

    def test_timeout_and_direct_interruption_reap_child(self):
        for termination in (None, signal.SIGTERM, signal.SIGINT):
            with self.subTest(termination=termination):
                pidfile = self.root / "child.pid"
                pidfile.unlink(missing_ok=True)
                self.fake("import os, pathlib, time\npathlib.Path(" + repr(str(pidfile)) +
                          ").write_text(str(os.getpid()))\ntime.sleep(30)\n")
                code = ("import importlib.util,sys; s=importlib.util.spec_from_file_location('r',sys.argv[1]);"
                        "m=importlib.util.module_from_spec(s);s.loader.exec_module(m);"
                        "sys.exit(m.run_once(sys.argv[2],sys.argv[4:],1,sys.argv[3]))")
                process = subprocess.Popen([sys.executable, "-B", "-c", code, str(Path(runner.__file__).resolve()),
                                            str(self.state), str(self.binary), *self.args],
                                           stdout=subprocess.PIPE, stderr=subprocess.PIPE)
                try:
                    deadline = time.monotonic() + 5
                    while not pidfile.exists() and process.poll() is None and time.monotonic() < deadline:
                        time.sleep(0.01)
                    self.assertTrue(pidfile.exists(), "test child never started")
                    child = int(pidfile.read_text())
                    if termination is not None:
                        process.send_signal(termination)
                    stdout, stderr = process.communicate(timeout=5)
                    self.assertEqual(process.returncode, 1, (stdout, stderr))
                    self.assertEqual((stdout, stderr), (b"", b""))
                    self.assertEqual(self.status()["state"], "timed_out" if termination is None else "interrupted")
                    with self.assertRaises(ProcessLookupError):
                        os.kill(child, 0)
                    self.fake("print(" + repr(json.dumps(self.response(blocked_reason="risk_halted"))) + ")")
                    self.assertEqual(self.execute(), 0, "interruption retained a runner lock")
                finally:
                    if process.poll() is None:
                        process.kill()
                        process.wait()

    def test_cleanup_ignores_repeated_signals_and_reports_errors(self):
        self.fake("print(" + repr(json.dumps(self.response(blocked_reason="risk_halted"))) + ")")
        original = os.killpg

        def repeated(pid, sig):
            self.assertEqual(signal.getsignal(signal.SIGTERM), signal.SIG_IGN)
            self.assertEqual(signal.getsignal(signal.SIGINT), signal.SIG_IGN)
            os.kill(os.getpid(), signal.SIGTERM)
            return original(pid, sig)

        with patch.object(runner.os, "killpg", side_effect=repeated):
            self.assertEqual(self.execute(), 0)
        with patch.object(runner.os, "killpg", side_effect=OSError("private-secret")):
            self.assertEqual(self.execute(), 1)
        self.assertEqual(self.status()["state"], "failed")
        self.assertNotIn("private-secret", json.dumps(self.status()))


if __name__ == "__main__":
    unittest.main()
