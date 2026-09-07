import pathlib
import shutil
import subprocess
import unittest


class AttemptMarkerTests(unittest.TestCase):
    def run_loop(self, mode):
        source = pathlib.Path(__file__).with_name("run-market-scout.sh").read_text()
        loop = source[source.index("attempt=1\nwhile :; do"):source.index("run_started_epoch=$(")]
        # Run the real loop without touching runtime files or invoking Hermes.
        loop = loop.replace('/usr/bin/rm -f "$packet_error"', ':')
        loop = loop.replace('/usr/bin/date', 'false' if mode == 'clock-failure' else shutil.which('date'))
        script = '''set -eu
mode=$1
packet_error=unused
packet_envelope_hint() { :; }
collect_research_packet() {
  case "$mode" in
    interrupted) kill -TERM $$ ;;
    double-failure) return 7 ;;
    retry) [ "$attempt" -eq 2 ] ;;
    success|clock-failure) return 0 ;;
  esac
}
''' + loop
        result = subprocess.run(["/bin/sh", "-c", script, "test", mode], capture_output=True, text=True)
        self.assertEqual(result.stdout, "")
        markers = [line for line in result.stderr.splitlines() if line.startswith("mithril-hermes-attempt-v1 ")]
        parsed = [dict(field.split("=", 1) for field in line.split()[1:]) for line in markers]
        for row in parsed:
            self.assertEqual(row["phase"], "prepublication")
            self.assertIn(row["attempt"], ("1", "2"))
            if mode == "clock-failure":
                self.assertEqual(row["started_at"], "unavailable")
                if row["event"] == "END":
                    self.assertEqual(row["ended_at"], "unavailable")
            else:
                self.assertGreater(int(row["started_at"]), 0)
                if row["event"] == "END":
                    self.assertGreaterEqual(int(row["ended_at"]), int(row["started_at"]))
            self.assertNotIn("sha256", row)
        self.assertNotIn("session", "\n".join(markers))
        self.assertNotIn("INVOCATION_ID", loop)
        self.assertNotIn("publication_success", "\n".join(markers))
        return result, parsed

    def test_first_failure_second_success(self):
        result, rows = self.run_loop("retry")
        self.assertEqual(result.returncode, 0)
        self.assertEqual([row["event"] for row in rows], ["START", "END", "START", "END"])
        self.assertEqual([row.get("exit_status") for row in rows], [None, "1", None, "0"])
        self.assertEqual([row["attempt"] for row in rows], ["1", "1", "2", "2"])

    def test_double_failure(self):
        result, rows = self.run_loop("double-failure")
        self.assertEqual(result.returncode, 7)
        self.assertEqual([row["exit_status"] for row in rows if row["event"] == "END"], ["7", "7"])

    def test_interrupted_has_no_invented_end(self):
        result, rows = self.run_loop("interrupted")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual([row["event"] for row in rows], ["START"])

    def test_success_only_proves_prepublication(self):
        result, rows = self.run_loop("success")
        self.assertEqual(result.returncode, 0)
        self.assertEqual(len(rows), 2)
        self.assertEqual(rows[-1]["exit_status"], "0")

    def test_unavailable_clock_preserves_research_result(self):
        result, rows = self.run_loop("clock-failure")
        self.assertEqual(result.returncode, 0)
        self.assertEqual(len(rows), 2)
        self.assertEqual(rows[-1]["exit_status"], "0")


if __name__ == "__main__":
    unittest.main()
