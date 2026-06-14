#!/usr/bin/env python3
"""Fire-and-forget Codex app-server dispatcher for GoCell PR review labels.

The dispatcher owns only GitHub discovery, local de-duplication, and starting
Codex app-server turns. It deliberately does not wait for the turn to complete:
the project-local pr-review skill is responsible for PR comments, machine
blocks, and label transitions.

Run this as a long-lived process for real dispatching. It owns one
`codex app-server --stdio` child; exiting the dispatcher closes that app-server.
The `--once` flag is only for dry runs, smoke tests, and stubbed selftests.
"""

from __future__ import annotations

import argparse
import concurrent.futures
import json
import os
import signal
import subprocess
import sys
import threading
import time
from dataclasses import dataclass
from pathlib import Path
from typing import Any


REVIEW_LABEL = "pr-status/needs-review-again"
CHECK_LABEL = "pr-status/needs-check-fix"
REPO_DEFAULT = "ghbvf/gocell"


class DispatchError(Exception):
    """A recoverable per-PR dispatch failure."""


@dataclass(frozen=True)
class Config:
    repo_root: Path
    router_home: Path
    repo: str
    authors: frozenset[str]
    interval: int
    codex_bin: str
    gh_bin: str
    dry_run: bool

    @property
    def state_dir(self) -> Path:
        return self.router_home / "state"

    @property
    def locks_dir(self) -> Path:
        return self.router_home / "locks"

    @property
    def logs_dir(self) -> Path:
        return self.router_home / "logs"

    @property
    def ledger_file(self) -> Path:
        return self.state_dir / "dispatched"

    @property
    def skill_path(self) -> Path:
        return self.repo_root / ".codex" / "skills" / "pr-review" / "SKILL.md"


@dataclass(frozen=True)
class Candidate:
    number: int
    head_sha: str
    head_ref: str
    author: str
    is_cross_repository: bool
    is_draft: bool
    kind: str  # review | check

    @property
    def key(self) -> str:
        return f"{self.number}@{self.head_sha}:{self.kind}"

    @property
    def skill_command(self) -> str:
        if self.kind == "check":
            return f"pr-review {self.number} --check"
        return f"pr-review {self.number}"


def log(message: str) -> None:
    ts = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    print(f"[{ts}] {message}", flush=True)


def fail(message: str, code: int = 1) -> None:
    print(f"router: {message}", file=sys.stderr)
    raise SystemExit(code)


def run_json(args: list[str], *, input_text: str | None = None) -> Any:
    proc = subprocess.run(
        args,
        input=input_text,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    if proc.returncode != 0:
        stderr = proc.stderr.strip()
        raise DispatchError(f"{' '.join(args)} failed ({proc.returncode}): {stderr}")
    output = proc.stdout.strip()
    if not output:
        return None
    try:
        return json.loads(output)
    except json.JSONDecodeError as exc:
        raise DispatchError(f"{' '.join(args)} returned invalid JSON: {exc}") from exc


def run_text(args: list[str]) -> str:
    proc = subprocess.run(
        args,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        check=False,
    )
    if proc.returncode != 0:
        stderr = proc.stderr.strip()
        raise DispatchError(f"{' '.join(args)} failed ({proc.returncode}): {stderr}")
    return proc.stdout.strip()


def parse_bool(value: Any) -> bool:
    if isinstance(value, bool):
        return value
    if isinstance(value, str):
        return value.lower() == "true"
    return bool(value)


def load_config(args: argparse.Namespace) -> Config:
    repo_root = Path(
        os.environ.get("GOCELL_APP_ROUTER_REPO_ROOT", Path.cwd())
    ).resolve()
    router_home = Path(
        os.environ.get(
            "GOCELL_APP_ROUTER_HOME",
            str(Path.home() / ".local" / "gocell-pr-app-router"),
        )
    ).resolve()
    authors_raw = os.environ.get("GOCELL_APP_ROUTER_AUTHORS", "")
    authors = frozenset(a for a in authors_raw.split() if a)
    if not authors:
        fail("GOCELL_APP_ROUTER_AUTHORS is required")

    interval_raw = os.environ.get("GOCELL_APP_ROUTER_INTERVAL", "120")
    try:
        interval = int(interval_raw)
    except ValueError as exc:
        raise SystemExit(f"router: invalid GOCELL_APP_ROUTER_INTERVAL={interval_raw!r}") from exc
    if interval <= 0:
        fail("GOCELL_APP_ROUTER_INTERVAL must be positive")

    cfg = Config(
        repo_root=repo_root,
        router_home=router_home,
        repo=os.environ.get("GOCELL_APP_ROUTER_REPO", REPO_DEFAULT),
        authors=authors,
        interval=interval,
        codex_bin=os.environ.get("CODEX_BIN", "codex"),
        gh_bin=os.environ.get("GH_BIN", "gh"),
        dry_run=args.dry_run,
    )
    for path in (cfg.state_dir, cfg.locks_dir, cfg.logs_dir):
        path.mkdir(parents=True, exist_ok=True)
    if not cfg.skill_path.exists():
        fail(f"pr-review skill not found: {cfg.skill_path}")
    cfg.ledger_file.touch(exist_ok=True)
    return cfg


def gh_pr_list(cfg: Config, label: str) -> list[dict[str, Any]]:
    fields = "number,headRefName,headRefOid,author,isCrossRepository,isDraft"
    data = run_json(
        [
            cfg.gh_bin,
            "pr",
            "list",
            "--repo",
            cfg.repo,
            "--state",
            "open",
            "--label",
            label,
            "--json",
            fields,
        ]
    )
    if not isinstance(data, list):
        raise DispatchError(f"gh pr list for {label} returned non-list JSON")
    return data


def gh_live_head(cfg: Config, pr: int) -> str:
    return run_text(
        [
            cfg.gh_bin,
            "pr",
            "view",
            str(pr),
            "--repo",
            cfg.repo,
            "--json",
            "headRefOid",
            "--jq",
            ".headRefOid",
        ]
    )


def candidate_from_json(raw: dict[str, Any], kind: str) -> Candidate:
    author = raw.get("author") or {}
    login = author.get("login") if isinstance(author, dict) else ""
    return Candidate(
        number=int(raw["number"]),
        head_sha=str(raw["headRefOid"]),
        head_ref=str(raw.get("headRefName", "")),
        author=str(login or ""),
        is_cross_repository=parse_bool(raw.get("isCrossRepository", False)),
        is_draft=parse_bool(raw.get("isDraft", False)),
        kind=kind,
    )


def discover_candidates(cfg: Config) -> list[Candidate]:
    review_raw = gh_pr_list(cfg, REVIEW_LABEL)
    check_raw = gh_pr_list(cfg, CHECK_LABEL)

    by_pr: dict[int, Candidate] = {}
    conflicts: set[int] = set()
    for raw in review_raw:
        cand = candidate_from_json(raw, "review")
        by_pr[cand.number] = cand
    for raw in check_raw:
        cand = candidate_from_json(raw, "check")
        if cand.number in by_pr:
            conflicts.add(cand.number)
        by_pr[cand.number] = cand

    out: list[Candidate] = []
    for cand in sorted(by_pr.values(), key=lambda c: c.number):
        if cand.number in conflicts:
            log(f"PR #{cand.number}: skip - both review and check trigger labels are present")
            continue
        out.append(cand)
    return out


def ledger_contains(cfg: Config, key: str) -> bool:
    try:
        with cfg.ledger_file.open("r", encoding="utf-8") as fh:
            return any(line.rstrip("\n") == key for line in fh)
    except FileNotFoundError:
        return False


def ledger_append(cfg: Config, key: str) -> None:
    with cfg.ledger_file.open("a", encoding="utf-8") as fh:
        fh.write(key + "\n")


def pid_alive(pid: int) -> bool:
    try:
        os.kill(pid, 0)
        return True
    except ProcessLookupError:
        return False
    except PermissionError:
        return True


class PrLock:
    def __init__(self, cfg: Config, pr: int) -> None:
        self.path = cfg.locks_dir / f"{pr}.lock"
        self.pid_file = self.path / "pid"
        self.acquired = False

    def __enter__(self) -> "PrLock":
        try:
            self.path.mkdir()
        except FileExistsError:
            self._reclaim_if_stale()
            try:
                self.path.mkdir()
            except FileExistsError:
                return self
        self.pid_file.write_text(str(os.getpid()), encoding="utf-8")
        self.acquired = True
        return self

    def _reclaim_if_stale(self) -> None:
        try:
            raw = self.pid_file.read_text(encoding="utf-8").strip()
            pid = int(raw)
        except (FileNotFoundError, ValueError):
            return
        if not pid_alive(pid):
            try:
                self.pid_file.unlink()
                self.path.rmdir()
            except OSError:
                pass

    def __exit__(self, _exc_type: object, _exc: object, _tb: object) -> None:
        if not self.acquired:
            return
        try:
            self.pid_file.unlink()
        except FileNotFoundError:
            pass
        try:
            self.path.rmdir()
        except OSError:
            pass


class AppServerClient:
    def __init__(self, cfg: Config) -> None:
        self.cfg = cfg
        self.proc: subprocess.Popen[str] | None = None
        self.reader: Any = None
        self.writer: Any = None
        self.next_id = 1
        self.lock = threading.Lock()

    def __enter__(self) -> "AppServerClient":
        self.proc = subprocess.Popen(
            [self.cfg.codex_bin, "app-server", "--stdio"],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=None,
            text=True,
            bufsize=1,
        )
        return self

    def __exit__(self, _exc_type: object, _exc: object, _tb: object) -> None:
        if self.proc is None:
            return
        if self.proc.stdin:
            try:
                self.proc.stdin.close()
            except BrokenPipeError:
                pass
        if self.proc.poll() is None:
            try:
                self.proc.wait(timeout=2)
                return
            except subprocess.TimeoutExpired:
                pass
        if self.proc.poll() is None:
            self.proc.terminate()
            try:
                self.proc.wait(timeout=2)
            except subprocess.TimeoutExpired:
                self.proc.kill()
            self.proc.wait(timeout=2)

    def request(self, method: str, params: dict[str, Any]) -> Any:
        if self.proc is None or self.proc.stdin is None or self.proc.stdout is None:
            raise DispatchError("app-server is not running")
        with self.lock:
            req_id = self.next_id
            self.next_id += 1
            payload = {"id": req_id, "method": method, "params": params}
            try:
                self.proc.stdin.write(json.dumps(payload, separators=(",", ":")) + "\n")
                self.proc.stdin.flush()
            except BrokenPipeError as exc:
                raise DispatchError("app-server closed stdin") from exc

            while True:
                line = self.proc.stdout.readline()
                if line == "":
                    raise DispatchError(f"app-server exited before {method} response")
                try:
                    msg = json.loads(line)
                except json.JSONDecodeError:
                    continue
                if msg.get("id") != req_id:
                    continue
                if "error" in msg:
                    raise DispatchError(f"{method} failed: {msg['error']}")
                return msg.get("result")

    def notify(self, method: str, params: dict[str, Any]) -> None:
        if self.proc is None or self.proc.stdin is None:
            raise DispatchError("app-server is not running")
        with self.lock:
            payload = {"method": method, "params": params}
            try:
                self.proc.stdin.write(json.dumps(payload, separators=(",", ":")) + "\n")
                self.proc.stdin.flush()
            except BrokenPipeError as exc:
                raise DispatchError("app-server closed stdin") from exc


def initialize_app_server(client: AppServerClient) -> None:
    client.request(
        "initialize",
        {
            "clientInfo": {
                "name": "gocell-pr-app-dispatcher",
                "title": "GoCell PR App Dispatcher",
                "version": "1",
            },
            "capabilities": {"experimentalApi": True},
        },
    )


def start_pr_review_turn(cfg: Config, client: AppServerClient, cand: Candidate) -> tuple[str, str]:
    prompt = (
        "Use the attached local project skill `pr-review` exactly. "
        f"Execute `{cand.skill_command}` for repository `{cfg.repo}`. "
        "Complete the full skill workflow, including posting the pm:pr-review "
        "comment, appending the machine block, and actually applying the label "
        "transition required by the skill. Do not stop at a label-transition "
        "suggestion. Do not use the built-in Codex review command."
    )
    thread_params = {
        "cwd": str(cfg.repo_root),
        "approvalPolicy": "never",
        "sandbox": "workspace-write",
        "runtimeWorkspaceRoots": [str(cfg.repo_root)],
    }
    turn_params_base = {
        "approvalPolicy": "never",
        "cwd": str(cfg.repo_root),
        "runtimeWorkspaceRoots": [str(cfg.repo_root)],
        "sandboxPolicy": {
            "type": "workspaceWrite",
            "networkAccess": True,
            "writableRoots": [str(cfg.repo_root), str(cfg.router_home)],
        },
        "input": [
            {
                "type": "skill",
                "name": "pr-review",
                "path": str(cfg.skill_path),
            },
            {
                "type": "text",
                "text": prompt,
                "text_elements": [],
            },
        ],
    }
    thread_result = client.request("thread/start", thread_params)
    thread_id = ((thread_result or {}).get("thread") or {}).get("id")
    if not thread_id:
        raise DispatchError("thread/start response did not include thread.id")
    turn_params = dict(turn_params_base)
    turn_params["threadId"] = thread_id
    turn_result = client.request("turn/start", turn_params)
    turn_id = ((turn_result or {}).get("turn") or {}).get("id")
    if not turn_id:
        raise DispatchError("turn/start response did not include turn.id")
    return thread_id, turn_id


def should_skip(cfg: Config, cand: Candidate) -> str | None:
    if cand.is_cross_repository:
        return "cross-repository PR"
    if cand.is_draft:
        return "draft PR"
    if cand.author not in cfg.authors:
        return f"author {cand.author!r} not in allowlist"
    if ledger_contains(cfg, cand.key):
        return f"already dispatched key {cand.key}"
    return None


def dispatch_one(cfg: Config, client: AppServerClient | None, cand: Candidate) -> bool:
    with PrLock(cfg, cand.number) as lock:
        if not lock.acquired:
            log(f"PR #{cand.number}: skip - locked")
            return False

        skip = should_skip(cfg, cand)
        if skip:
            log(f"PR #{cand.number}: skip - {skip}")
            return False

        live_head = gh_live_head(cfg, cand.number)
        if live_head != cand.head_sha:
            log(
                f"PR #{cand.number}: skip - head moved "
                f"(listed={cand.head_sha[:12]} live={live_head[:12]})"
            )
            return False

        if cfg.dry_run:
            log(f"PR #{cand.number}: dry-run would dispatch {cand.kind} ({cand.key})")
            return False
        if client is None:
            raise DispatchError("app-server client is required outside dry-run")

        log(f"PR #{cand.number}: dispatching {cand.kind} via Codex app-server ({cand.key})")
        thread_id, turn_id = start_pr_review_turn(cfg, client, cand)
        ledger_append(cfg, cand.key)
        log(
            f"PR #{cand.number}: dispatched {cand.kind}; "
            f"thread={thread_id} turn={turn_id}; ledger key recorded"
        )
        return True


def poll_once(cfg: Config, client: AppServerClient | None) -> int:
    candidates = discover_candidates(cfg)
    if not candidates:
        log("poll_once: no candidates")
        return 0

    dispatched = 0
    workers = len(candidates)
    with concurrent.futures.ThreadPoolExecutor(max_workers=workers) as pool:
        futures = {pool.submit(dispatch_one, cfg, client, cand): cand for cand in candidates}
        for future in concurrent.futures.as_completed(futures):
            cand = futures[future]
            try:
                if future.result():
                    dispatched += 1
            except Exception as exc:  # noqa: BLE001 - daemon logs per-PR failures and continues.
                log(f"PR #{cand.number}: dispatch failed - {exc}")
    log(f"poll_once: done candidates={len(candidates)} dispatched={dispatched}")
    return dispatched


def parse_args(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--once",
        action="store_true",
        help="run one poll and exit; for smoke tests only when not using --dry-run",
    )
    parser.add_argument("--dry-run", action="store_true", help="do not start Codex or write ledger")
    return parser.parse_args(argv)


def main(argv: list[str]) -> int:
    args = parse_args(argv)
    cfg = load_config(args)
    if cfg.dry_run:
        poll_once(cfg, None)
        return 0
    with AppServerClient(cfg) as client:
        initialize_app_server(client)
        if args.once:
            log(
                "--once will exit after dispatch and close app-server; "
                "production dispatch should run without --once"
            )
            poll_once(cfg, client)
            return 0
        while True:
            try:
                poll_once(cfg, client)
            except Exception as exc:  # noqa: BLE001 - daemon should keep polling.
                log(f"poll failed - {exc}")
            time.sleep(cfg.interval)


if __name__ == "__main__":
    signal.signal(signal.SIGPIPE, signal.SIG_DFL)
    raise SystemExit(main(sys.argv[1:]))
