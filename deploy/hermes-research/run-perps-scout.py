#!/usr/bin/python3
"""Run isolated Hermes synthesis over host-verified, paper-only perps evidence."""

import argparse
import fcntl
import hashlib
import importlib.util
import json
import math
import os
from pathlib import Path
import pwd
import signal
import shutil
import stat
import subprocess
import sys
import time
import uuid


SCRIPT = Path(__file__).resolve()
SPEC = importlib.util.spec_from_file_location(
    "research_evidence", SCRIPT.with_name("build-research-evidence.py"),
)
evidence = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(evidence)

ROOT = Path("/var/lib/mithril-hermes-perps-research")
RUNTIME = Path("/run/mithril-hermes-perps-research")
DEPLOY = Path("/opt/mithril-hermes-research")
STATE = Path("/var/lib/mithril-agent-perps-paper/current")
AGENT = "/usr/local/libexec/mithril-agent/mithril-agent"
USER = "mithril-agent-research"
DASHBOARD = Path("/var/lib/mithril-agent-dashboard/perps-proposals.json")
SYMBOLS = ("SOL", "BTC", "ETH")
IMAGE = "nousresearch/hermes-agent:v2026.8.27@sha256:e0df6adebddf29b91112aefc999d4aaf6846c9eb544faca5672a16a13590ff79"
CONTAINER_LABEL = "io.mithril.perps-proposal=1"


class ContainerCleanupError(RuntimeError):
    """Stop the batch when the host cannot confirm container removal."""


class RunInterrupted(BaseException):
    """Unwind the current phase without starting another paper proposal."""


def interrupt_run(signum, frame):
    raise RunInterrupted()


def sha256(raw):
    return hashlib.sha256(raw).hexdigest()


def make_prompt(raw, symbol):
    context = evidence.strict_json_object(raw)
    digest = context.get("content_sha256")
    if (context.get("status") != "advisory_context" or context.get("symbol") != symbol
            or context.get("paper_only") is not True or context.get("authorized") is not False
            or context.get("promotable") is not False or not isinstance(digest, str)
            or not evidence.SHA256.fullmatch(digest)):
        raise ValueError("host context envelope is invalid")
    hypothesis = "hermes-" + digest[:48]
    prompt = (
        "Propose one bounded paper-only strategy experiment from the verified host context below. "
        "All training and holdout metrics shown here are already historical, not unseen validation. "
        "Compare risk, drawdown, fees and completed trades across the recorded trials and resolved "
        "prior proposals. Losses count; pending or unscored attempts are not profitable evidence. "
        "Do not invent current news, prices, fills or sources. You have no tools. "
        "Prefer retaining the baseline when the evidence does not support a change. "
        "Your proposal will be frozen before a separately assigned later paper attempt; it cannot "
        "activate a strategy, change limits, trade or access a wallet. "
        "Return exactly one JSON object with only hypothesis_id, symbol, risk_arm, strategy, rationale. "
        f"Copy hypothesis_id={hypothesis} and symbol={symbol} exactly. "
        "risk_arm must be conservative, balanced or experimental. strategy must be momentum, "
        "mean_reversion, breakout or regime. Give a short single-line rationale (1–2000 UTF-8 bytes), "
        "including the main limitation of the evidence. No Markdown or additional output.\n\n"
        "HOST_CONTEXT_JSON\n" + raw.decode("utf-8").rstrip("\n")
    )
    return context, hypothesis, prompt


def extract_bound_proposal(sessions, prompt, context_raw, symbol, started, finished):
    context, hypothesis, expected_prompt = make_prompt(context_raw, symbol)
    if prompt != expected_prompt:
        raise ValueError("proposal prompt differs from exact host context")
    raw = evidence.extract_packet(sessions, started, finished, require_no_tools=True)
    records = [evidence.strict_json_object(line) for line in sessions.splitlines() if line.strip()]
    # This one-turn profile has no delegation or compression. Do not accept an
    # unrelated completed session merely because its final JSON looks plausible.
    if len(records) != 1:
        raise ValueError("perps proposal requires one isolated session")
    users = [m for m in records[0]["messages"] if m.get("role") == "user"]
    if len(users) != 1 or users[0].get("content") != expected_prompt:
        raise ValueError("session does not contain the exact host prompt")
    proposal = evidence.strict_json_object(raw)
    if (set(proposal) != {"hypothesis_id", "symbol", "risk_arm", "strategy", "rationale"}
            or proposal.get("hypothesis_id") != hypothesis or proposal.get("symbol") != symbol):
        raise ValueError("proposal changed its host-bound identity")
    receipt = {
        "version": 1, "status": "model_output_verified", "paper_only": True,
        "authorized": False, "promotable": False, "symbol": symbol,
        "context_sha256": context["content_sha256"], "context_file_sha256": sha256(context_raw),
        "prompt_sha256": sha256(prompt.encode()), "session_export_sha256": sha256(sessions),
        "proposal_input_sha256": sha256(raw), "session_id": records[0]["id"],
        "run_started": started, "run_finished": finished, "image": IMAGE,
        "provider": "openai-codex", "model": "gpt-5.6-terra", "tool_calls": 0,
    }
    return raw, receipt


def as_research(*args, timeout=120):
    with subprocess.Popen(
        ["/usr/sbin/runuser", "-u", USER, "--", *map(str, args)],
        stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, start_new_session=True,
    ) as process:
        try:
            output, _ = process.communicate(timeout=timeout)
        except (subprocess.TimeoutExpired, RunInterrupted):
            os.killpg(process.pid, signal.SIGKILL)
            process.communicate()
            raise
        if process.returncode:
            raise subprocess.CalledProcessError(process.returncode, args)
        return output


def container(env, name, *args, timeout):
    command = ["/usr/bin/docker", "compose", "run", "--rm", "--no-TTY", "--name", name,
               "--label", CONTAINER_LABEL, "hermes-perps-proposal", *args]
    try:
        subprocess.run(command, cwd=DEPLOY, env=env, check=True, timeout=timeout,
                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    finally:
        # A client timeout must not leave this run's model container behind.
        try:
            subprocess.run(["/usr/bin/docker", "rm", "--force", name], check=False, timeout=30,
                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            remaining = subprocess.run(
                ["/usr/bin/docker", "ps", "-aq", "--filter", "name=^/" + name + "$"],
                check=True, timeout=30, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
            )
            if remaining.stdout.strip():
                raise ContainerCleanupError("paper research container is still present")
        except (OSError, subprocess.SubprocessError) as error:
            raise ContainerCleanupError("paper research container removal is unverified") from error


def cleanup_containers():
    if os.geteuid() != 0:
        raise ValueError("container cleanup requires the host administrator")
    command = ["/usr/bin/docker", "ps", "-aq", "--filter", "label=" + CONTAINER_LABEL]
    result = subprocess.run(command, check=True, timeout=30, stdout=subprocess.PIPE,
                            stderr=subprocess.DEVNULL)
    identifiers = result.stdout.decode("ascii").split()
    if any(not identifier or len(identifier) > 64 or any(c not in "0123456789abcdef" for c in identifier)
           for identifier in identifiers):
        raise ContainerCleanupError("container cleanup returned invalid identities")
    if identifiers:
        subprocess.run(["/usr/bin/docker", "rm", "--force", *identifiers], check=True, timeout=30,
                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    remaining = subprocess.run(command, check=True, timeout=30, stdout=subprocess.PIPE,
                               stderr=subprocess.DEVNULL)
    if remaining.stdout.strip():
        raise ContainerCleanupError("paper research cleanup is incomplete")


def run_symbol(symbol, directory, home, identity, run_id, progress):
    data = directory / "evidence"
    data.mkdir(mode=0o700)
    os.chown(data, identity.pw_uid, identity.pw_gid)
    home.mkdir(mode=0o700)
    os.chown(home, identity.pw_uid, identity.pw_gid)
    context_path = data / "context.json"
    progress["phase"] = "prepare_context"
    raw = as_research(AGENT, "shadow", "perps-context", "--state-dir", STATE,
                      "--symbol", symbol, "--auto", "--out", context_path)
    context, _, prompt = make_prompt(raw, symbol)
    prompt_path = directory / "prompt.txt"
    with prompt_path.open("x", encoding="utf-8") as stream:
        stream.write(prompt)
    prompt_path.chmod(0o644)
    env = dict(os.environ, MITHRIL_UID=str(identity.pw_uid), MITHRIL_GID=str(identity.pw_gid),
               MITHRIL_HERMES_PERPS_HOME=str(home), MITHRIL_HERMES_PERPS_QUERY_FILE=str(prompt_path))
    started = time.time()
    progress["phase"] = "model_proposal"
    container(env, f"mithril-perps-{run_id}-{symbol.lower()}", timeout=150)
    finished = time.time()
    progress["phase"] = "export_session"
    container(env, f"mithril-perps-export-{run_id}-{symbol.lower()}",
              "sessions", "export", "--format", "jsonl", "--redact", "--yes",
              "/opt/research-data/sessions.jsonl", timeout=45)
    progress["phase"] = "verify_model_output"
    metadata = evidence.strict_json_object(as_research(
        "/usr/bin/python3", SCRIPT, "extract", "--sessions", home / "sessions.jsonl",
        "--context", context_path, "--prompt", prompt_path, "--symbol", symbol,
        "--data", data, "--started", str(started), "--finished", str(finished),
    ))
    args = [AGENT, "shadow", "perps-freeze", "--state-dir", str(STATE),
            "--in", str(data / "proposal.json"), "--context", str(context_path)]
    for tape in context["training"]:
        digest = tape["tape_sha256"]
        if not isinstance(digest, str) or not evidence.SHA256.fullmatch(digest):
            raise ValueError("host tape identity is invalid")
        args.extend(["--tape", str(STATE.parent / "tapes" / symbol.lower() / (digest + ".json"))])
    progress["phase"] = "freeze_proposal"
    frozen = evidence.strict_json_object(as_research(*args))
    if (frozen.get("context_sha256") != context["content_sha256"]
            or frozen.get("status") != "pending_advisory"
            or frozen.get("authorized") is not False or frozen.get("promotable") is not False):
        raise ValueError("freeze receipt differs from verified context")
    metadata.update(status="pending_advisory", proposal_sha256=frozen["content_sha256"],
                    target_episode=frozen["target_episode"], frozen_at=frozen["frozen_at"],
                    hypothesis_id=frozen["input"]["hypothesis_id"])
    progress["phase"] = "record_invocation"
    evidence.replace_private(directory / "invocation.json", json.dumps(metadata).encode() + b"\n")
    return {"symbol": symbol, "status": "pending_advisory", "target_episode": frozen["target_episode"],
            "context_sha256": context["content_sha256"], "proposal_sha256": frozen["content_sha256"],
            "strategy": frozen["input"]["strategy"], "risk_arm": frozen["input"]["risk_arm"],
            "training_tapes": len(context["training"]),
            "resolved_outcomes": 0 if context["resolved_outcomes"] is None else len(context["resolved_outcomes"])}


def private_invocation(path):
    info = path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid() or info.st_mode & 0o077:
        raise ValueError("private invocation is invalid")
    return evidence.strict_json_object(evidence.read_private(path, 64 << 10))


def create_invocation_receipt(path, value):
    # The enclosing archive is administrator-owned and held under run.lock.
    # Exclusive creation makes a crash residue unresolved, never permission to retry.
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
    with os.fdopen(descriptor, "wb") as stream:
        stream.write(json.dumps(value).encode() + b"\n")
        stream.flush()
        os.fsync(stream.fileno())
    parent = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        os.fsync(parent)
    finally:
        os.close(parent)


def reconcile_proposals(enabled):
    if not enabled:
        return  # perps-context --auto already resolves feedback without selection.
    pending = {symbol: [] for symbol in SYMBOLS}
    for archive in ROOT.iterdir():
        if len(archive.name) != 32 or any(c not in "0123456789abcdef" for c in archive.name):
            continue
        info = archive.lstat()
        if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.geteuid() or info.st_mode & 0o022:
            raise ValueError("private research archive is invalid")
        for symbol in SYMBOLS:
            directory = archive / symbol.lower()
            try:
                info = directory.lstat()
            except FileNotFoundError:
                continue
            if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.geteuid() or info.st_mode & 0o022:
                raise ValueError("private research archive is invalid")
            try:
                receipt = private_invocation(directory / "invocation.json")
            except FileNotFoundError:
                continue
            context, proposal = receipt.get("context_sha256"), receipt.get("proposal_sha256")
            if (receipt.get("version") != 1 or receipt.get("status") != "pending_advisory"
                    or receipt.get("symbol") != symbol or receipt.get("paper_only") is not True
                    or receipt.get("authorized") is not False or receipt.get("promotable") is not False
                    or not isinstance(context, str) or not evidence.SHA256.fullmatch(context)
                    or not isinstance(proposal, str) or not evidence.SHA256.fullmatch(proposal)):
                raise ValueError("private proposal identity is invalid")
            hypothesis = "hermes-" + context[:48]
            target = receipt.get("target_episode")
            started, finished = receipt.get("run_started"), receipt.get("run_finished")
            if (receipt.get("hypothesis_id", hypothesis) != hypothesis
                    or not isinstance(target, str) or not target.isascii() or not target.isdecimal()
                    or str(int(target)) != target or not 0 < int(target) < 1 << 64
                    or any(type(at) not in (int, float) or not math.isfinite(at) or at <= 0 for at in (started, finished))
                    or finished < started):
                raise ValueError("private proposal chronology is invalid")
            pending[symbol].append((started, archive.name, directory, receipt, hypothesis))
            if len(pending[symbol]) > 256:
                raise ValueError("private proposal archive exceeds bound")
    for symbol in SYMBOLS:
        seen = set()
        for _, _, directory, receipt, hypothesis in sorted(pending[symbol]):
            if receipt["proposal_sha256"] in seen:
                continue
            seen.add(receipt["proposal_sha256"])
            intent = directory / "selection-attempt.json"
            completed = directory / "selection-result.json"
            # A malformed or unresolved intent is never retried automatically.
            if intent.exists() or intent.is_symlink() or completed.exists() or completed.is_symlink():
                try:
                    for marker in (intent, completed):
                        if not marker.exists() and not marker.is_symlink():
                            continue
                        saved = private_invocation(marker)
                        statuses = ("selection_attempted",) if marker == intent else (
                            "unevaluable", "evaluated_proposal_not_selected", "qualified_paper_plan_selected",
                            "qualified_paper_plan_already_selected", "qualified_paper_plan_retired")
                        digest = saved.get("evaluation_sha256")
                        if (type(saved.get("version")) is not int or saved["version"] != 1
                                or saved.get("status") not in statuses or not isinstance(digest, str)
                                or not evidence.SHA256.fullmatch(digest)
                                or saved.get("symbol") != symbol or saved.get("proposal_sha256") != receipt["proposal_sha256"]
                                or saved.get("target_episode") != receipt["target_episode"]
                                or saved.get("paper_only") is not True or saved.get("authorized") is not False
                                or saved.get("promotable") is not False):
                            raise ValueError("selection marker identity is invalid")
                    if not completed.exists():
                        raise ValueError("selection outcome is unresolved")
                except (ValueError, OSError):
                    print("perps selection requires manual reconciliation; private intent retained", file=sys.stderr)
                continue
            path = STATE.parent / "proposals" / symbol.lower() / (hypothesis + ".json")
            try:
                raw = as_research(AGENT, "shadow", "perps-evaluate", "--proposal", path, timeout=30)
                if len(raw) > 64 << 10:
                    raise ValueError("evaluation output exceeds bound")
                outcome = evidence.strict_json_object(raw)
                if (outcome.get("proposal_sha256") != receipt["proposal_sha256"]
                        or outcome.get("target_episode") != receipt["target_episode"]
                        or outcome.get("paper_only") is not True or outcome.get("authorized") is not False
                        or outcome.get("promotable") is not False
                        or outcome.get("status") not in ("pending", "evaluated", "unevaluable")):
                    raise ValueError("evaluated proposal identity is invalid")
                if outcome["status"] == "pending":
                    break
                digest = outcome.get("content_sha256")
                if not isinstance(digest, str) or not evidence.SHA256.fullmatch(digest):
                    raise ValueError("evaluated proposal digest is invalid")
                bound = {"version": 1, "paper_only": True, "authorized": False, "promotable": False,
                         "symbol": symbol, "proposal_sha256": receipt["proposal_sha256"],
                         "evaluation_sha256": digest, "target_episode": receipt["target_episode"]}
                if outcome["status"] == "unevaluable":
                    create_invocation_receipt(completed, dict(bound, status="unevaluable"))
                    break
                create_invocation_receipt(intent, dict(bound, status="selection_attempted"))
                raw = as_research(AGENT, "shadow", "perps-select-proposal", "--proposal", path, timeout=30)
                if len(raw) > 64 << 10:
                    raise ValueError("selection output exceeds bound")
                selected = evidence.strict_json_object(raw)
                proof = selected.get("evaluated_proposal", {})
                expected_proof = dict(version=1, proposal_sha256=bound["proposal_sha256"],
                    evaluation_sha256=digest, context_sha256=receipt["context_sha256"],
                    target_episode=bound["target_episode"], frozen_at=receipt["frozen_at"],
                    observed_at=outcome["observed_at"], start_sha256=outcome["start_sha256"],
                    terminal_sha256=outcome["terminal_sha256"])
                if (selected.get("status") not in ("evaluated_proposal_not_selected", "qualified_paper_plan_selected",
                        "qualified_paper_plan_already_selected", "qualified_paper_plan_retired")
                        or selected.get("symbol") != symbol or selected.get("paper_only") is not True
                        or selected.get("authorized") is not False or selected.get("promotable") is not False
                        or selected.get("execution_enabled") is not False or proof != expected_proof
                        or selected.get("strategy") != outcome["proposed"]["strategy"]
                        or selected.get("risk_arm") != outcome["proposed"]["risk_arm"]):
                    raise ValueError("selection receipt identity is invalid")
                result = dict(bound, status=selected["status"])
                changed = selected["status"] == "qualified_paper_plan_selected"
                if selected.get("pointer_updated") is not changed or selected.get("rollback_updated") is not changed:
                    raise ValueError("selection update flags are invalid")
                if selected["status"] != "evaluated_proposal_not_selected" and "plan_sha256" not in selected:
                    raise ValueError("selected plan identity is missing")
                if "plan_sha256" in selected:
                    digest = selected["plan_sha256"]
                    if not isinstance(digest, str) or not evidence.SHA256.fullmatch(digest):
                        raise ValueError("selection plan identity is invalid")
                    result["plan_sha256"] = digest
                create_invocation_receipt(completed, result)
            except (ValueError, KeyError, OSError, subprocess.SubprocessError):
                print("perps proposal reconciliation incomplete; private receipts retained", file=sys.stderr)
            # At most one outstanding proposal per market per cycle, never rank returns.
            break


def publish_dashboard(status):
    identity = pwd.getpwnam("mithril-agent-dashboard")
    fields = ("symbol", "status", "phase", "target_episode", "context_sha256", "proposal_sha256",
              "strategy", "risk_arm", "training_tapes", "resolved_outcomes")
    projection = {key: status[key] for key in
                  ("version", "paper_only", "authorized", "promotable", "run_id", "finished_at")}
    projection["markets"] = [{key: row[key] for key in fields if key in row} for row in status["markets"]]
    if DASHBOARD.parent.resolve() != DASHBOARD.parent:
        raise ValueError("dashboard directory is invalid")
    parent = os.open(DASHBOARD.parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    temporary = ".perps-proposals-" + uuid.uuid4().hex
    try:
        info = os.fstat(parent)
        if info.st_uid != identity.pw_uid or info.st_mode & 0o022:
            raise ValueError("dashboard directory is invalid")
        try:
            info = os.stat(DASHBOARD.name, dir_fd=parent, follow_symlinks=False)
        except FileNotFoundError:
            pass
        else:
            if not stat.S_ISREG(info.st_mode) or info.st_uid != identity.pw_uid or info.st_mode & 0o077:
                raise ValueError("dashboard destination is invalid")
        fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=parent)
        with os.fdopen(fd, "wb") as stream:
            stream.write(json.dumps(projection).encode() + b"\n")
            stream.flush()
            os.fchown(stream.fileno(), identity.pw_uid, identity.pw_gid)
            os.fsync(stream.fileno())
        os.replace(temporary, DASHBOARD.name, src_dir_fd=parent, dst_dir_fd=parent)
        os.fsync(parent)
    finally:
        try:
            try:
                os.unlink(temporary, dir_fd=parent)
            except FileNotFoundError:
                pass
        finally:
            os.close(parent)


def run():
    if os.geteuid() != 0:
        raise ValueError("the fixed container launcher requires the host administrator")
    selection = os.environ.get("MITHRIL_HERMES_PERPS_SELECT", "0")
    if selection not in ("0", "1"):
        raise ValueError("paper proposal selection flag is invalid")
    identity = pwd.getpwnam(USER)
    # StateDirectory and RuntimeDirectory are root-owned. Only isolated child
    # homes and host evidence files belong to the research identity.
    for path in (ROOT, RUNTIME):
        info = path.lstat()
        if not path.is_dir() or path.is_symlink() or info.st_uid != 0 or info.st_mode & 0o022:
            raise ValueError("research root is not administrator-controlled")
    # Preserve disk for the existing paper collectors. Never delete retained
    # outcomes or trading history to make room for another model experiment.
    if min(shutil.disk_usage(path).free for path in (ROOT, RUNTIME)) < 1 << 30:
        raise ValueError("paper research needs at least 1 GiB of free space")
    with (ROOT / "run.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        reconcile_proposals(selection == "1")
        run_id = uuid.uuid4().hex
        archive = ROOT / run_id
        archive.mkdir(mode=0o711)
        archive.chmod(0o711)
        results = []
        for symbol in SYMBOLS:
            directory = archive / symbol.lower()
            directory.mkdir(mode=0o711)
            directory.chmod(0o711)
            progress = {"symbol": symbol, "phase": "prepare_directories"}
            try:
                result = run_symbol(symbol, directory, RUNTIME / (run_id + "-" + symbol.lower()), identity, run_id, progress)
            except ContainerCleanupError:
                results.append(dict(progress, status="cleanup_required"))
                break
            except RunInterrupted:
                results.append(dict(progress, status="interrupted"))
                break
            except (ValueError, KeyError, OSError, subprocess.SubprocessError):
                result = dict(progress, status="unavailable")
            results.append(result)
        status = {"version": 1, "paper_only": True, "authorized": False, "promotable": False,
                  "run_id": run_id, "finished_at": evidence.rfc3339nano_epoch(time.time()), "markets": results}
        evidence.replace_private(ROOT / "latest.json", json.dumps(status).encode() + b"\n")
        print(json.dumps(status))
        try:
            publish_dashboard(status)
        except (ValueError, OSError, KeyError):
            print("perps proposal dashboard publication failed; private receipt retained", file=sys.stderr)
            raise
        if any(result["status"] != "pending_advisory" for result in results):
            raise ValueError("one or more paper proposal phases were unavailable")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    subcommands = parser.add_subparsers(dest="command", required=True)
    subcommands.add_parser("run")
    subcommands.add_parser("cleanup")
    extract = subcommands.add_parser("extract")
    for name in ("sessions", "context", "prompt", "data"):
        extract.add_argument("--" + name, type=Path, required=True)
    extract.add_argument("--symbol", choices=SYMBOLS, required=True)
    extract.add_argument("--started", type=float, required=True)
    extract.add_argument("--finished", type=float, required=True)
    args = parser.parse_args()
    if args.command == "run":
        signal.signal(signal.SIGTERM, interrupt_run)
        signal.signal(signal.SIGINT, interrupt_run)
        run()
        return
    if args.command == "cleanup":
        cleanup_containers()
        return
    sessions = evidence.read_private(args.sessions, evidence.MAX_EXPORT_BYTES)
    context = evidence.read_private(args.context, 256 << 10)
    with args.prompt.open("rb") as stream:
        prompt_raw = stream.read((256 << 10) + 4097)
    if len(prompt_raw) > (256 << 10) + 4096:
        raise ValueError("host prompt exceeds bound")
    prompt = prompt_raw.decode("utf-8")
    proposal, receipt = extract_bound_proposal(sessions, prompt, context, args.symbol, args.started, args.finished)
    evidence.replace_private(args.data / "sessions.jsonl", sessions)
    evidence.replace_private(args.data / "proposal.json", proposal)
    evidence.replace_private(args.data / "model-output.json", json.dumps(receipt).encode() + b"\n")
    print(json.dumps(receipt))


if __name__ == "__main__":
    try:
        main()
    except (ValueError, KeyError, OSError, subprocess.SubprocessError, ContainerCleanupError, RunInterrupted):
        print("perps research did not complete; retained evidence is private", file=sys.stderr)
        sys.exit(1)
