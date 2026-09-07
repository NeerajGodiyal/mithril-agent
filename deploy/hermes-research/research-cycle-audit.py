#!/usr/bin/env python3
"""Offline measurement of ONE externally pinned completed research cycle.

Manual usefulness protocol (not computed by this program): before collecting a
comparison, freeze the question, eligible cycles, time budget and exclusions.
Blind reviewers to treatment and model identity. Separately mark whether each
finding is supported by retained sources, relevant to the frozen question, and
not already supplied in the host context; retain contradictions and abstentions.
Adjudicate disagreements. Retrieval counts, packet acceptance and URL novelty
are not usefulness scores. Historical screening is not forward strategy proof.
No treatment effect can be estimated without actual comparable treatment runs.
"""
import argparse
import hashlib
import importlib.util
import json
from pathlib import Path
import sys


SPEC = importlib.util.spec_from_file_location("research_evidence", Path(__file__).with_name("build-research-evidence.py"))
evidence = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(evidence)


def audit(sessions, packet, stored, pins, started, finished):
    if len(pins) != 3:
        raise ValueError("pins")
    for raw, pin in zip((sessions, packet, stored), pins):
        if not isinstance(pin, str) or not evidence.SHA256.fullmatch(pin) or hashlib.sha256(raw).hexdigest() != pin:
            raise ValueError("identity")
    sealed = evidence.strict_json_object(packet)
    saved = evidence.strict_json_object(stored)
    recomputed = evidence.build_evidence(sessions, packet, started, finished)
    # Canonical encoding distinguishes JSON booleans from numeric counters.
    canonical = lambda value: json.dumps(value, sort_keys=True, separators=(",", ":"), allow_nan=False)
    if canonical(saved) != canonical(recomputed):
        raise ValueError("evidence mismatch")
    final_raw = evidence.extract_packet(sessions, started, finished)
    bound = evidence.strict_json_object(evidence.bind_source_times(sessions, final_raw, started, finished))
    normalized = dict(sealed)
    normalized.pop("content_sha256", None)
    normalized.pop("recorded_observations", None)
    if type(bound.get("version")) is not int or bound["version"] not in (1, 2) or canonical(bound) != canonical(normalized):
        raise ValueError("final packet binding")
    rows = [evidence.strict_json_object(line) for line in sessions.splitlines() if line.strip()]
    low = min(row["started_at"] for row in rows)
    high = max(row["ended_at"] for row in rows)
    disposition = sealed.get("disposition")
    if disposition not in ("candidate", "no_change", "blocked"):
        raise ValueError("disposition")
    return {"version": 1, "kind": "single_archived_research_cycle_audit", "read_only": True,
            "authorized": False, "promotable": False, "identities_verified": True,
            "final_packet_binding_verified": True, "go_packet_validation_performed": False,
            "sessions_sha256": pins[0], "packet_file_sha256": pins[1], "evidence_sha256": pins[2],
            "packet_content_sha256": sealed["content_sha256"], "created_at": sealed["created_at"],
            "disposition": disposition, "session_count": recomputed["session_count"],
            "tool_calls": recomputed["tool_calls"], "successful_web_searches": recomputed["successful_web_searches"],
            "retrieved_url_count": len(recomputed["retrieved_urls"]),
            "cited_retrieved_url_count": recomputed["official_pages_checked"],
            "exported_session_envelope_seconds": high-low, "supplied_run_interval_seconds": finished-started,
            "latency_basis": "exported sessions, not model-only time; supplied interval provenance is external; host preparation not inferred",
            "archived_successful_attempts": 1, "all_attempts": None, "scheduled_opportunities": None,
            "failed_attempts": None, "usefulness": None,
            "limitations": "Successful archive only; missing attempts unknown. Retrieval and citation counts do not establish independent organizations, research quality, model learning or profitable strategy performance."}


class Arguments(argparse.ArgumentParser):
    def error(self, message):
        raise ValueError("arguments")


def main():
    parser = Arguments()
    for name in ("sessions", "packet", "evidence"):
        parser.add_argument("--"+name, required=True)
        parser.add_argument("--"+name+"-sha256", required=True)
    parser.add_argument("--run-started", type=float, required=True)
    parser.add_argument("--run-finished", type=float, required=True)
    args = parser.parse_args()
    result = audit(evidence.read_private(Path(args.sessions), 32 << 20),
                   evidence.read_private(Path(args.packet), 64 << 10),
                   evidence.read_private(Path(args.evidence), 128 << 10),
                   (args.sessions_sha256, args.packet_sha256, args.evidence_sha256), args.run_started, args.run_finished)
    print(json.dumps(result, sort_keys=True, separators=(",", ":"), allow_nan=False))


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, TypeError, KeyError, OverflowError, RecursionError):
        print("research cycle audit unavailable", file=sys.stderr)
        sys.exit(1)
