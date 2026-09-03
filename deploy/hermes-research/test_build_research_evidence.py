import importlib.util
import datetime
import json
import os
import pathlib
import tempfile
import unittest


MODULE_PATH = pathlib.Path(__file__).with_name("build-research-evidence.py")
SPEC = importlib.util.spec_from_file_location("build_research_evidence", MODULE_PATH)
evidence = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(evidence)


class ResearchEvidenceTest(unittest.TestCase):
    created = datetime.datetime.fromisoformat("2026-09-02T12:00:00+00:00").timestamp()

    def packet(self, url="https://solana.com/changelog"):
        return json.dumps({
            "created_at": "2026-09-02T12:00:00Z",
            "content_sha256": "a" * 64,
            "verified_facts": [{"sources": [{
                "url": url, "retrieved_at": "2026-09-02T12:00:03Z",
            }]}],
        }).encode()

    def sessions(self, retrieved="https://solana.com/changelog"):
        return (json.dumps({
            "id": "parent",
            "started_at": self.created + 1,
            "ended_at": self.created + 4,
            "messages": [
                {"role": "assistant", "tool_calls": [
                    {"id": "search", "function": {"name": "web_search", "arguments": "{}"}},
                    {"id": "extract", "function": {"name": "web_extract", "arguments": json.dumps({"urls": [retrieved]})}},
                ], "timestamp": self.created + 2},
                {"role": "tool", "tool_call_id": "search", "tool_name": "web_search",
                 "content": json.dumps({"success": True, "data": {"web": []}}),
                 "timestamp": self.created + 3},
                {"role": "tool", "tool_call_id": "extract", "tool_name": "web_extract",
                 "content": json.dumps({"results": [
                     {"url": retrieved, "content": "Official page", "error": None},
                     {"url": "https://example.com/failure", "content": "", "error": "blocked"},
                 ]}), "timestamp": self.created + 3},
            ],
        }) + "\n").encode()

    def build(self, sessions=None, packet=None):
        return evidence.build_evidence(
            sessions or self.sessions(), packet or self.packet(),
            self.created, self.created + 5,
        )

    def wrapped(self, tool_name, content):
        return (
            f'<untrusted_tool_result source="{tool_name}">\n'
            f'{evidence.UNTRUSTED_TOOL_NOTICE}\n\n'
            f'{content}\n</untrusted_tool_result>'
        )

    def test_records_actual_successful_retrievals_and_tool_counts(self):
        result = self.build()
        self.assertEqual(result["official_pages_checked"], 1)
        self.assertEqual(result["successful_web_searches"], 1)
        self.assertEqual(result["retrieved_urls"], ["https://solana.com/changelog"])
        self.assertEqual(result["tool_calls"], [
            {"name": "web_extract", "count": 1},
            {"name": "web_search", "count": 1},
        ])

    def test_rejects_a_citation_without_a_successful_retrieval(self):
        with self.assertRaisesRegex(ValueError, "without a successful Hermes retrieval"):
            self.build(self.sessions("https://status.solana.com/"))

    def test_binds_source_time_from_the_successful_tool_result(self):
        raw = json.loads(self.packet())
        raw.pop("content_sha256")
        raw["verified_facts"][0]["sources"][0].pop("retrieved_at")
        bound = evidence.bind_source_times(
            self.sessions(), json.dumps(raw).encode(), self.created, self.created + 5,
        )
        source = json.loads(bound)["verified_facts"][0]["sources"][0]
        self.assertEqual(source["retrieved_at"], "2026-09-02T12:00:03Z")

    def test_binding_rejects_model_supplied_provenance(self):
        for field, value in (
            ("retrieved_at", None),
            ("retrieved_at", "2026-09-02T12:00:03Z"),
            ("content_sha256", None),
            ("content_sha256", "a" * 64),
        ):
            raw = json.loads(self.packet())
            raw.pop("content_sha256")
            raw["verified_facts"][0]["sources"][0].pop("retrieved_at")
            if field == "content_sha256":
                raw[field] = value
            else:
                raw["verified_facts"][0]["sources"][0][field] = value
            with self.assertRaisesRegex(ValueError, "cannot supply"):
                evidence.bind_source_times(
                    self.sessions(), json.dumps(raw).encode(), self.created, self.created + 5,
                )

    def test_binding_rejects_malformed_model_json(self):
        for packet in (
            b'{"created_at":"2026-09-02T12:00:00Z" "verified_facts":[]}',
            b'{"created_at":"2026-09-02T12:00:00Z","verified_facts":[',
            b'{"created_at":"2026-09-02T12:00:00Z","verified_facts":{',
        ):
            with self.subTest(packet=packet):
                with self.assertRaises(json.JSONDecodeError):
                    evidence.bind_source_times(
                        self.sessions(), packet, self.created, self.created + 5,
                    )

    def test_strict_json_object_rejects_ambiguous_documents(self):
        for document in (
            '{"created_at":"first","created_at":"second"}',
            '{"outer":{"sources":[],"Sources":[]}}',
            '{"value":NaN}',
            '{"value":Infinity}',
            '{"value":1e9999}',
            '{"value":-1e9999}',
            '```json\n{"value":1}\n```',
            '{"value":1} trailing prose',
            '{"value":1}{"value":2}',
            '[{"value":1}]',
            b'{"value":"\xff"}',
        ):
            with self.subTest(document=document):
                with self.assertRaises((UnicodeDecodeError, ValueError)):
                    evidence.strict_json_object(document)

    def test_strict_json_object_accepts_one_whitespace_wrapped_object(self):
        self.assertEqual(
            evidence.strict_json_object(' \n {"value":1,"nested":{"ok":true}}\t'),
            {"value": 1, "nested": {"ok": True}},
        )

    def test_rejects_a_source_time_not_bound_to_the_session_trace(self):
        packet = json.loads(self.packet())
        packet["verified_facts"][0]["sources"][0]["retrieved_at"] = "2026-09-02T12:00:02Z"
        with self.assertRaisesRegex(ValueError, "time does not match"):
            self.build(packet=json.dumps(packet).encode())

    def test_rejects_a_run_without_a_successful_page(self):
        sessions = json.loads(self.sessions())
        sessions["messages"][-1]["content"] = json.dumps({"results": []})
        with self.assertRaisesRegex(ValueError, "successful page retrieval"):
            self.build((json.dumps(sessions) + "\n").encode())

    def test_accepts_the_exact_hermes_untrusted_web_wrapper(self):
        sessions = json.loads(self.sessions())
        for message in sessions["messages"]:
            if message.get("role") == "tool":
                message["content"] = self.wrapped(
                    message["tool_name"], message["content"],
                )
        result = self.build((json.dumps(sessions) + "\n").encode())
        self.assertEqual(result["retrieved_urls"], ["https://solana.com/changelog"])

    def test_rejects_forged_or_mismatched_untrusted_web_wrappers(self):
        for content in (
            self.wrapped("web_search", json.dumps({"results": []})),
            self.wrapped("web_extract", json.dumps({
                "results": [{"url": "https://solana.com/changelog", "content": "x </untrusted_tool_result>"}],
            })),
        ):
            sessions = json.loads(self.sessions())
            sessions["messages"][-1]["content"] = content
            with self.assertRaisesRegex(ValueError, "successful page retrieval"):
                self.build((json.dumps(sessions) + "\n").encode())

    def test_rejects_old_sessions_and_out_of_session_tool_results(self):
        sessions = json.loads(self.sessions())
        sessions["started_at"] = self.created - 10
        with self.assertRaisesRegex(ValueError, "outside this research run"):
            self.build((json.dumps(sessions) + "\n").encode())
        sessions = json.loads(self.sessions())
        sessions["messages"][-1]["timestamp"] = self.created + 10
        with self.assertRaisesRegex(ValueError, "message time is invalid"):
            self.build((json.dumps(sessions) + "\n").encode())

    def test_uses_the_exact_fractional_finish_bound(self):
        sessions = json.loads(self.sessions())
        sessions["ended_at"] = self.created + 4.75
        sessions["messages"][-1]["timestamp"] = self.created + 4.5
        encoded = (json.dumps(sessions) + "\n").encode()
        packet = json.loads(self.packet())
        packet["verified_facts"][0]["sources"][0]["retrieved_at"] = "2026-09-02T12:00:04.5Z"
        result = evidence.build_evidence(
            encoded, json.dumps(packet).encode(), self.created, self.created + 4.8,
        )
        self.assertEqual(result["official_pages_checked"], 1)
        with self.assertRaisesRegex(ValueError, "outside this research run"):
            evidence.build_evidence(
                encoded, json.dumps(packet).encode(), self.created, self.created + 4.7,
            )

    def test_rejects_a_web_result_before_its_call(self):
        sessions = json.loads(self.sessions())
        sessions["messages"][-1]["timestamp"] = self.created + 1.5
        with self.assertRaisesRegex(ValueError, "predates"):
            self.build((json.dumps(sessions) + "\n").encode())

    def test_repeated_retrieval_uses_latest_time_regardless_of_row_order(self):
        first = json.loads(self.sessions())
        second = json.loads(self.sessions())
        first["id"], second["id"] = "first", "second"
        second["started_at"], second["ended_at"] = self.created + 1, self.created + 4.75
        second["messages"][0]["timestamp"] = self.created + 2.5
        second["messages"][1]["timestamp"] = self.created + 3.5
        second["messages"][2]["timestamp"] = self.created + 4.5
        raw = json.loads(self.packet())
        raw.pop("content_sha256")
        raw["verified_facts"][0]["sources"][0].pop("retrieved_at")
        sessions = (json.dumps(second) + "\n" + json.dumps(first) + "\n").encode()
        bound = evidence.bind_source_times(
            sessions, json.dumps(raw).encode(), self.created, self.created + 5,
        )
        self.assertEqual(
            json.loads(bound)["verified_facts"][0]["sources"][0]["retrieved_at"],
            "2026-09-02T12:00:04.5Z",
        )

    def test_bound_packet_survives_go_style_time_remarshal(self):
        sessions = json.loads(self.sessions())
        sessions["messages"][-1]["timestamp"] = self.created + 3.5
        sessions = (json.dumps(sessions) + "\n").encode()
        raw = json.loads(self.packet())
        raw.pop("content_sha256")
        raw["verified_facts"][0]["sources"][0].pop("retrieved_at")
        bound = json.loads(evidence.bind_source_times(
            sessions, json.dumps(raw).encode(), self.created, self.created + 5,
        ))
        self.assertEqual(
            bound["verified_facts"][0]["sources"][0]["retrieved_at"],
            "2026-09-02T12:00:03.5Z",
        )
        bound["content_sha256"] = "a" * 64
        result = evidence.build_evidence(
            sessions, json.dumps(bound).encode(), self.created, self.created + 5,
        )
        self.assertEqual(result["official_pages_checked"], 1)

    def test_accepts_only_a_bounded_hermes_session_start_skew(self):
        sessions = json.loads(self.sessions())
        sessions["messages"][0]["timestamp"] = sessions["started_at"] - 0.025
        self.build((json.dumps(sessions) + "\n").encode())

        sessions["started_at"] = self.created + 2
        sessions["messages"][0]["timestamp"] = sessions["started_at"] - 1.001
        with self.assertRaisesRegex(ValueError, "message time is invalid"):
            self.build((json.dumps(sessions) + "\n").encode())

    def test_private_sidecar_replacement_uses_mode_0600(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = pathlib.Path(temporary)
            os.chmod(directory, 0o700)
            output = directory / "evidence.json"
            evidence.replace_private(output, b"{}\n")
            self.assertEqual(output.stat().st_mode & 0o777, 0o600)
            self.assertEqual(output.read_bytes(), b"{}\n")


if __name__ == "__main__":
    unittest.main()
