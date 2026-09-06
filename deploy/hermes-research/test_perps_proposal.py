import importlib.util
import pathlib
import unittest
from unittest.mock import patch


SPEC = importlib.util.spec_from_file_location(
    "perps_proposal", pathlib.Path(__file__).with_name("perps-proposal.py"))
launcher = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(launcher)


class PerpsProposalIsolationTest(unittest.TestCase):
    def test_arbitrary_launcher_commands_rejected_before_home_access(self):
        for args in (["chat"], ["sessions", "export", "/tmp/other.jsonl"]):
            with self.subTest(args=args), patch.object(launcher.sys, "argv", ["launcher", *args]), \
                    patch.object(launcher.os, "umask"), \
                    self.assertRaisesRegex(RuntimeError, "export arguments"):
                launcher.main()

    def test_guard_checks_post_init_and_before_conversation(self):
        calls = []

        class Agent:
            def __init__(self, tools, **kwargs):
                self.tools = tools
                self.kwargs = kwargs

            def run_conversation(self, prompt):
                calls.append(prompt)
                return "result"

        launcher.install_guard(Agent)
        for invalid in (None, {}, ["late_context_tool"]):
            with self.subTest(tools=invalid), self.assertRaisesRegex(RuntimeError, "isolation"):
                Agent(invalid)
        agent = Agent([], skip_memory=False, max_iterations=60)
        self.assertEqual(agent.kwargs, {
            "skip_context_files": True, "skip_memory": True,
            "skip_background_review": True, "load_soul_identity": False,
            "max_iterations": 1,
        })
        self.assertEqual(agent.run_conversation("exact prompt"), "result")
        agent.tools.append("late_tool")
        with self.assertRaisesRegex(RuntimeError, "isolation"):
            agent.run_conversation("must not run")
        self.assertEqual(calls, ["exact prompt"])


if __name__ == "__main__":
    unittest.main()
