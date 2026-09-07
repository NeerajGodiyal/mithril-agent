#!/usr/bin/env python3
"""Run one serialized unsigned strategy step and publish a private status."""

import argparse
from contextlib import ExitStack
import datetime
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import stat
import subprocess
import tempfile
import time


AGENT = "/usr/local/libexec/mithril-agent/mithril-agent"
PATH_FLAGS = {
    "--strategy", "--inventory", "--authority-policy", "--submitter-policy",
    "--next-authority-policy", "--buy-authority-policy", "--sell-authority-policy",
    "--next-submitter-policy", "--historical-submitter-policy",
}
AGE_FLAGS = {"--max-decision-age-seconds", "--max-acquisition-age-seconds"}
BLOCKED = {
    "claim_not_prepared", "claim_not_reserved", "acquisition_pending",
    "wallet_has_pending_claim", "wallet_strategy_mismatch", "risk_halted",
}


def checked_args(args):
    if len(args) % 2 or len(args) > 160:
        raise ValueError("invalid step arguments")
    values = {}
    for flag, value in zip(args[::2], args[1::2]):
        if flag in values and flag != "--historical-submitter-policy":
            raise ValueError("duplicate step argument")
        if flag in PATH_FLAGS:
            if not os.path.isabs(value) or os.path.normpath(value) != value or "\0" in value:
                raise ValueError("invalid step path")
        elif flag in AGE_FLAGS:
            if not re.fullmatch(r"[1-9][0-9]{0,9}", value):
                raise ValueError("invalid step age")
        else:
            raise ValueError("unsupported step argument")
        values[flag] = value
    if not {"--strategy", "--inventory", "--authority-policy", "--submitter-policy"} <= values.keys():
        raise ValueError("missing original step inputs")
    return values


def protected_directory(path):
    path = Path(path)
    if not path.is_absolute() or str(path) != os.path.normpath(str(path)):
        raise ValueError("invalid directory")
    for current in (path, *path.parents):
        info = current.lstat()
        shared_tmp = current != path and info.st_uid == 0 and info.st_mode & stat.S_ISVTX
        if (not stat.S_ISDIR(info.st_mode) or info.st_uid not in (0, os.geteuid())
                or (info.st_mode & 0o022 and not shared_tmp)):
            raise ValueError("unprotected directory")
    if path.stat().st_mode & 0o077:
        raise ValueError("runner directory must be private")
    return path


def timestamp(value):
    return datetime.datetime.fromtimestamp(value, datetime.timezone.utc).isoformat()


def publish(directory, record):
    # Replace only the runner's own status; original journals are never outputs.
    fd, temporary = tempfile.mkstemp(prefix=".status-", dir=directory)
    try:
        with os.fdopen(fd, "w") as output:
            json.dump(record, output, sort_keys=True)
            output.write("\n")
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, directory / "status.json")
        directory_fd = os.open(directory, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(directory_fd)
        finally:
            os.close(directory_fd)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def project(result):
    if not isinstance(result, dict) or result.get("can_sign") is not False or result.get("can_submit") is not False:
        raise ValueError("unsigned result required")
    if result.get("status") == "unsigned_claim_not_authorized":
        if type(result.get("pending")) is not bool or type(result.get("recovered")) is not bool:
            raise ValueError("invalid claim state")
        record = {"state": "awaiting_finality" if result["pending"] else "blocked",
                  "pending": result["pending"], "recovered": result["recovered"]}
        if not result["pending"]:
            record["blocked_reason"] = "claim_not_reserved"
        head = result.get("head_sha256")
    elif result.get("status") == "strategy_step_not_authorized":
        if type(result.get("strategy_pending")) is not bool:
            raise ValueError("invalid strategy state")
        reason = result.get("blocked_reason", "")
        if reason and reason not in BLOCKED:
            raise ValueError("unknown blocked state")
        record = {"pending": result["strategy_pending"]}
        if reason:
            record.update(state="blocked", blocked_reason=reason)
        else:
            decision = result.get("observation_decision")
            if result["strategy_pending"] or not isinstance(decision, dict) or decision.get("ReadyForQuote") is not False:
                raise ValueError("missing no-opportunity observation")
            record["state"] = "observed_no_opportunity"
        head = result.get("current_head_sha256")
    else:
        raise ValueError("unknown step result")
    if not isinstance(head, str) or not re.fullmatch(r"[0-9a-f]{64}", head):
        raise ValueError("invalid verified head")
    record["head_sha256"] = head
    return record


def run_once(state_dir, args, timeout_seconds=60, binary=AGENT):
    """Reuse CLI validation/admission; never invoke a signer or repair command."""
    if type(timeout_seconds) is not int or not 1 <= timeout_seconds <= 300:
        raise ValueError("timeout must be between 1 and 300 seconds")
    values = checked_args(args)
    directory = protected_directory(state_dir)
    inventory = Path(values["--inventory"])
    protected_directory(inventory.parent)
    status_path = str(directory / "status.json")
    lock_path = str(inventory) + ".unsigned-runner.lock"
    if any(value in (status_path, lock_path, str(directory / ".runner.lock"))
           for flag, value in zip(args[::2], args[1::2]) if flag in PATH_FLAGS):
        raise ValueError("runner output overlaps a protected input")
    with ExitStack() as locks:
        for target in (lock_path, directory / ".runner.lock"):
            fd = os.open(target, os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW | os.O_CLOEXEC, 0o600)
            lock = locks.enter_context(os.fdopen(fd, "rb"))
            info = os.fstat(lock.fileno())
            if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1 or info.st_uid != os.geteuid() or info.st_mode & 0o077:
                raise ValueError("unsafe runner lock")
            try:
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError:
                # A competing invocation must not overwrite the active run's status.
                return 75
        started = time.time()
        record = {"version": 1, "mode": "unsigned", "state": "running",
                  "started_at": timestamp(started), "deadline_at": timestamp(started + timeout_seconds),
                  "configuration_sha256": hashlib.sha256(json.dumps(args).encode()).hexdigest(),
                  "can_sign": False, "can_submit": False}
        publish(directory, record)
        code = 1
        def interrupt(signum, frame):
            raise InterruptedError("runner interrupted")

        previous = {sig: signal.signal(sig, interrupt) for sig in (signal.SIGTERM, signal.SIGINT)}
        try:
            with tempfile.TemporaryFile(dir=directory) as output:
                with subprocess.Popen([binary, "proposal", "strategy", "--operation", "step", *args],
                                      stdout=output, stderr=subprocess.DEVNULL, start_new_session=True) as process:
                    try:
                        exit_code = process.wait(timeout=timeout_seconds)
                    except subprocess.TimeoutExpired:
                        record.update(state="timed_out", error="step_timeout")
                    else:
                        if exit_code != 0:
                            record.update(state="failed", error="step_failed")
                        else:
                            output.seek(0)
                            raw = output.read(65537)
                            if len(raw) > 65536:
                                raise ValueError("step output too large")
                            record.update(project(json.loads(raw)))
                            code = 0
                    finally:
                        # Reap before releasing either lock, including direct interruption.
                        for sig in previous:
                            signal.signal(sig, signal.SIG_IGN)
                        try:
                            os.killpg(process.pid, signal.SIGKILL)
                        except ProcessLookupError:
                            pass
                        process.wait()
        except InterruptedError:
            code = 1
            record.update(state="interrupted", error="step_interrupted")
        except (OSError, ValueError):
            code = 1
            record.update(state="failed", error="invalid_or_unavailable_step")
        finally:
            for sig, handler in previous.items():
                signal.signal(sig, handler)
        record["finished_at"] = timestamp(time.time())
        publish(directory, record)
        return code


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--state-dir", required=True)
    parser.add_argument("--timeout-seconds", type=int, default=60)
    parser.add_argument("step_args", nargs=argparse.REMAINDER)
    options = parser.parse_args()
    args = options.step_args
    if args[:1] == ["--"]:
        args = args[1:]
    try:
        return run_once(options.state_dir, args, options.timeout_seconds)
    except (OSError, ValueError):
        parser.exit(1, "unsigned runner configuration or status storage unavailable\n")


if __name__ == "__main__":
    raise SystemExit(main())
