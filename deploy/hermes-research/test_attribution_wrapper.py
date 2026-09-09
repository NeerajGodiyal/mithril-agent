import pathlib
import subprocess
import tempfile
import unittest


class AttributionWrapperTests(unittest.TestCase):
    def exercise(self, mode):
        source = pathlib.Path(__file__).with_name("run-market-scout.sh").read_text()
        helper = source[source.index("attribution_diagnostic() ("):source.index("replay_rejection_hint() {")]
        block = source[source.index("  sol_attribution=unavailable"):source.index("  sol_behavior=unavailable")]
        self.assertGreater(source.index("  sol_attribution=unavailable"), source.index("collect_research_packet() ("))
        with tempfile.TemporaryDirectory() as directory:
            root = pathlib.Path(directory)
            binary = root / "binary"
            binary.write_text("synthetic executable")
            binary.chmod(0o500)
            if mode == "symlink":
                binary.rename(root / "target")
                binary.symlink_to(root / "target")
            helper = helper.replace("/opt/mithril-hermes-research/mithril-agent-attribution", str(binary))
            helper = helper.replace("/run/mithril-hermes-research/attribution.XXXXXX", str(root / "capture.XXXXXX"))
            helper = helper.replace("/usr/bin/rm", "/bin/rm").replace("/usr/bin/cat", "/bin/cat")
            for command in ("stat", "sha256sum", "date", "readlink", "prlimit"):
                helper = helper.replace("/usr/bin/" + command, "fake_" + command)
            script = r'''set -eu
umask 077
root=$1
mode=$2
generation=$root/generation
selector=fixed
sol_policy=sol-policy
sol_journals=sol-base
jup_policy=jup-policy
jup_journals=jup-base
research_query=$root/prompt
MITHRIL_HERMES_ATTRIBUTION_BINARY_SHA256=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
[ "$mode" != missing-pin ] || unset MITHRIL_HERMES_ATTRIBUTION_BINARY_SHA256
unset MITHRIL_AGENT_JUPITER_API_KEY
jupiter_api_key=SYNTHETIC_PRIVATE
fake_stat() {
  case "$2" in
    %u) if [ "$mode" = owner ]; then printf 1000; else printf 0; fi ;;
    %a) if [ "$mode" = writable ]; then printf 775; else printf 755; fi ;;
    %s) wc -c < "$4" ;;
  esac
}
fake_sha256sum() {
  if [ "$mode" = mismatch-pin ]; then printf b; else printf aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa; fi
  printf '  binary\n'
}
fake_readlink() {
  if [ "$mode" = selector-drift ] && [ -e "$root/called" ]; then printf changed; else printf '%s' "$generation"; fi
}
fake_date() {
  if [ "$mode" = day-drift ] && [ -e "$root/called" ]; then printf 2026-09-08; else printf 2026-09-07; fi
}
fake_prlimit() {
  [ "$1" = --fsize=16385:16385 ] && [ "$2" = -- ] && [ "$3" = /usr/bin/timeout ] &&
  [ "$4" = --kill-after=2s ] && [ "$5" = 60s ] && [ "$6" = /usr/sbin/runuser ] &&
  [ "$7" = -u ] && [ "$8" = mithril-agent-research ] && [ "$9" = -- ] || exit 9
  shift 9
  [ "$1" = "$root/binary" ] && [ "$2" = research ] &&
  [ "$4" = --policy ] && [ "$6" = --journal-dir ] && [ "$#" = 7 ] || exit 9
  case "$3" in attribution|cost-sensitivity) ;; *) exit 9;; esac
  case "$5:$7" in sol-policy:sol-base|jup-policy:jup-base) ;; *) exit 9;; esac
  /bin/sh -c '[ "${MITHRIL_AGENT_JUPITER_API_KEY+x}" != x ] && [ "${jupiter_api_key+x}" != x ]' || exit 9
  touch "$root/called"
  if [ "$mode" = timeout ]; then exit 124; fi
  if [ "$mode" = failed ] || { [ "$mode" = sol-failed ] && [ "$5" = sol-policy ]; }; then
    printf PRIVATE_PARTIAL; printf PRIVATE_ERROR >&2; return 1
  fi
  if [ "$mode" = overflow ]; then head -c 16385 /dev/zero; return; fi
  if [ "$mode" = empty ]; then return; fi
  if [ "$3" = cost-sensitivity ]; then
    printf '{"kind":"modelled_paper_cost_sensitivity","authorized":false}'
  else
    printf '{"net_change_micros":"-100","realized_micros":"20","coverage_sufficient":false,"fees_already_included":true}'
  fi
}
''' + helper + "\n" + block + '\ncat "$research_query"\n'
            result = subprocess.run(["/bin/sh", "-c", script, "test", directory, mode], capture_output=True, text=True, check=True)
            self.assertEqual(result.stderr, "")
            self.assertNotIn("PRIVATE", result.stdout)
            self.assertNotIn(directory, result.stdout)
            self.assertEqual(list(root.glob("capture.*")), [])
            return result.stdout

    def test_success_and_limits(self):
        output = self.exercise("success")
        self.assertEqual(output.count('"net_change_micros":"-100"'), 2)
        self.assertEqual(output.count('"kind":"modelled_paper_cost_sensitivity"'), 2)
        for phrase in ("BASE-book", "not current champion", "Fees are already included", "not causal", "do not compound", "new recorded basis"):
            self.assertIn(phrase, output)
        for phrase in ("not measured execution costs", "100 bps stress", "do not choose a cheaper assumption", "not a citation", "permission to change a policy"):
            self.assertIn(phrase, output)

    def test_unavailable_boundaries(self):
        for mode in ("missing-pin", "mismatch-pin", "owner", "writable", "symlink", "failed", "timeout", "overflow", "empty", "selector-drift", "day-drift"):
            with self.subTest(mode=mode):
                output = self.exercise(mode)
                self.assertIn("SOL/USDC: unavailable", output)
                if mode != "day-drift":
                    self.assertIn("JUP/USDC: unavailable", output)
                    self.assertNotIn('"kind":"modelled_paper_cost_sensitivity"', output)
                else:
                    # The next market starts after midnight and may verify its new day.
                    self.assertEqual(output.count('"net_change_micros":"-100"'), 1)

    @unittest.skipUnless(pathlib.Path("/usr/bin/prlimit").exists(), "Linux file-size limit")
    def test_native_capture_bound(self):
        with tempfile.TemporaryFile() as capture:
            result = subprocess.run([
                "/usr/bin/prlimit", "--fsize=16385:16385", "--",
                "/usr/bin/timeout", "--kill-after=2s", "60s",
                "/bin/sh", "-c", "head -c 1048576 /dev/zero",
            ], stdout=capture, stderr=subprocess.DEVNULL)
            self.assertNotEqual(result.returncode, 0)
            self.assertLessEqual(capture.tell(), 16385)

    def test_markets_fail_independently(self):
        output = self.exercise("sol-failed")
        self.assertIn("SOL/USDC: unavailable", output)
        self.assertEqual(output.count('"net_change_micros":"-100"'), 1)
        self.assertEqual(output.count('"kind":"modelled_paper_cost_sensitivity"'), 1)


if __name__ == "__main__":
    unittest.main()
