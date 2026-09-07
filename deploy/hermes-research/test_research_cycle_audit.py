import hashlib
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import unittest

import test_build_research_evidence as fixtures


SPEC = importlib.util.spec_from_file_location("cycle_audit", Path(__file__).with_name("research-cycle-audit.py"))
audit = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(audit)


class CycleAuditTest(unittest.TestCase):
    def setUp(self):
        f = fixtures.ResearchEvidenceTest()
        self.start, self.end = f.created, f.created+5
        packet = json.loads(f.packet())
        packet["version"] = 1
        packet["disposition"] = "no_change"
        raw_packet = json.loads(json.dumps(packet))
        del raw_packet["content_sha256"]
        for fact in raw_packet["verified_facts"]:
            for source in fact["sources"]:
                del source["retrieved_at"]
        self.sessions = f.sessions_with_final(json.dumps(raw_packet))
        self.packet = json.dumps(packet).encode()
        self.stored = json.dumps(fixtures.evidence.build_evidence(self.sessions, self.packet, self.start, self.end)).encode()

    def run_audit(self, sessions=None, stored=None, start=None, pins=None):
        values = (self.sessions if sessions is None else sessions, self.packet, self.stored if stored is None else stored)
        return audit.audit(*values, pins or tuple(hashlib.sha256(x).hexdigest() for x in values),
                           self.start if start is None else start, self.end)

    def test_measures_without_quality_or_attempt_claims(self):
        result = self.run_audit()
        self.assertEqual(result["exported_session_envelope_seconds"], 3)
        self.assertEqual(result["retrieved_url_count"], 1)
        self.assertEqual(result["cited_retrieved_url_count"], 1)
        self.assertIsNone(result["all_attempts"])
        self.assertIsNone(result["usefulness"])
        self.assertNotIn("https://", json.dumps(result))

    def test_identity_counts_and_metadata_tampering(self):
        with self.assertRaises(ValueError): self.run_audit(pins=("0"*64,)*3)
        for key, value in (("session_count", True), ("successful_web_searches", 999), ("packet_sha256", "0"*64), ("created_at", "2099-01-01T00:00:00Z")):
            altered = json.loads(self.stored); altered[key] = value
            with self.subTest(key=key), self.assertRaises(ValueError): self.run_audit(stored=json.dumps(altered).encode())

    def test_times_and_missing_root(self):
        for start in (self.start+2, float("nan"), float("inf")):
            with self.assertRaises(ValueError): self.run_audit(start=start)
        row = json.loads(self.sessions); row["source"] = "subagent"
        raw = json.dumps(row).encode()
        stored = json.dumps(fixtures.evidence.build_evidence(raw, self.packet, self.start, self.end)).encode()
        with self.assertRaises(ValueError): self.run_audit(sessions=raw, stored=stored)

    def test_same_time_different_model_packet_rejected(self):
        for field, value in (("disposition", "blocked"), ("verified_facts", [])):
            row = json.loads(self.sessions)
            final = json.loads(row["messages"][-1]["content"])
            final[field] = value
            row["messages"][-1]["content"] = json.dumps(final)
            raw = json.dumps(row).encode()
            stored = json.dumps(fixtures.evidence.build_evidence(raw, self.packet, self.start, self.end)).encode()
            with self.subTest(field=field), self.assertRaises(ValueError):
                self.run_audit(sessions=raw, stored=stored)

    def test_recorded_empty_facts_and_unsupported_null_or_bool_version(self):
        for version, facts, accepted in ((2, [], True), (2, None, False), (True, [], False)):
            row = json.loads(self.sessions)
            final = json.loads(row["messages"][-1]["content"])
            final.update(version=version, verified_facts=facts)
            row["messages"][-1]["content"] = json.dumps(final)
            sessions = json.dumps(row).encode()
            sealed = dict(final, content_sha256="a" * 64, recorded_observations={"host_marker": True})
            packet = json.dumps(sealed).encode()
            with self.subTest(version=version, facts=facts):
                if not accepted:
                    with self.assertRaises(ValueError):
                        stored = json.dumps(fixtures.evidence.build_evidence(sessions, packet, self.start, self.end)).encode()
                        audit.audit(sessions, packet, stored, tuple(hashlib.sha256(x).hexdigest() for x in (sessions, packet, stored)), self.start, self.end)
                else:
                    stored = json.dumps(fixtures.evidence.build_evidence(sessions, packet, self.start, self.end)).encode()
                    result = audit.audit(sessions, packet, stored, tuple(hashlib.sha256(x).hexdigest() for x in (sessions, packet, stored)), self.start, self.end)
                    self.assertTrue(result["final_packet_binding_verified"])
                    self.assertFalse(result["go_packet_validation_performed"])

    def test_cli_failure_is_static(self):
        result = subprocess.run([sys.executable, str(Path(__file__).with_name("research-cycle-audit.py")),
                                 "--sessions", "/private/PRIVATE-SENTINEL"], capture_output=True,
                                env=dict(os.environ, PYTHONDONTWRITEBYTECODE="1"))
        self.assertEqual(result.returncode, 1)
        self.assertEqual(result.stdout, b"")
        self.assertEqual(result.stderr, b"research cycle audit unavailable\n")


if __name__ == "__main__":
    unittest.main()
