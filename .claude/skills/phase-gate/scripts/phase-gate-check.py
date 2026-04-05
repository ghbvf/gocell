#!/usr/bin/env python3
"""
Phase Gate Check — fail-closed 阶段门检查。

Usage:
  python3 phase-gate-check.py --stage S0 --branch <branch> --check entry|exit

读取 phase-gates.yaml，校验 required_files + content_checks + repo_files + command_checks。
任何解析失败、未知阶段均视为 FAIL。空检查集 {} 除 S0 entry 外也视为 FAIL。
command_checks 受白名单约束（ALLOWED_CMD_PREFIXES），空命令视为 FAIL。
"""

import argparse
import os
import re
import subprocess
import sys
from datetime import datetime
from pathlib import Path

try:
    import yaml
except ImportError:
    print("ERROR: PyYAML is required. Install: pip3 install pyyaml")
    sys.exit(1)

# --- N/A 枚举值 ---
VALID_NA_REASONS = {"SCOPE_IRRELEVANT", "RESOURCE_UNAVAILABLE", "DEFERRED"}


def find_repo_root() -> Path:
    """从脚本位置回溯找到 repo root。"""
    script_dir = Path(__file__).resolve().parent
    # scripts/ → phase-gate/ → skills/ → .claude/ → repo_root
    root = script_dir.parents[3]
    if not (root / ".git").exists():
        print(f"ERROR: computed repo root has no .git directory: {root}")
        sys.exit(1)
    return root


def parse_args():
    p = argparse.ArgumentParser(description="Phase Gate Check (fail-closed)")
    p.add_argument("--stage", required=True, choices=[f"S{i}" for i in range(9)])
    p.add_argument("--branch", required=True)
    p.add_argument("--check", required=True, choices=["entry", "exit"])
    return p.parse_args()


def load_gates(gates_file: Path) -> dict:
    """加载 phase-gates.yaml。失败则 sys.exit(1)。"""
    if not gates_file.exists():
        print(f"ERROR: phase-gates.yaml not found: {gates_file}")
        sys.exit(1)
    try:
        with open(gates_file) as f:
            data = yaml.safe_load(f)
    except Exception as e:
        print(f"ERROR: YAML parse failed: {e}")
        sys.exit(1)
    if not isinstance(data, dict) or "stages" not in data:
        print("ERROR: phase-gates.yaml missing 'stages' key")
        sys.exit(1)
    return data


def get_stage_check(data: dict, stage: str, check_type: str) -> dict:
    """获取指定阶段的 entry/exit 配置。缺失则 FAIL。"""
    stages = data.get("stages", {})
    if stage not in stages:
        print(f"ERROR: stage '{stage}' not defined in phase-gates.yaml")
        sys.exit(1)
    stage_data = stages[stage]
    if check_type not in stage_data:
        print(f"ERROR: '{check_type}' not defined for {stage}")
        sys.exit(1)
    return stage_data[check_type]


def parse_na_declarations(charter_file: Path) -> dict[str, str]:
    """
    解析 phase-charter.md 中的 N/A 声明。
    格式: N/A:<REASON> <filename>  或  <filename> N/A:<REASON>
    返回 {filename: reason}
    """
    declarations = {}
    if not charter_file.exists():
        return declarations
    content = charter_file.read_text()
    # 匹配 N/A:SCOPE_IRRELEVANT spec.md 或 spec.md N/A:DEFERRED
    pattern = re.compile(
        r"^(?:[-*]\s+)?(?:N/A:(\w+)\s+([\w./\-]+))|(?:^(?:[-*]\s+)?([\w./\-]+)\s+N/A:(\w+))",
        re.IGNORECASE | re.MULTILINE,
    )
    for m in pattern.finditer(content):
        if m.group(1):
            reason, filename = m.group(1), m.group(2)
        else:
            filename, reason = m.group(3), m.group(4)
        reason_upper = reason.upper()
        if reason_upper in VALID_NA_REASONS:
            declarations[filename] = reason_upper
    return declarations


def check_required_files(
    files: list, specs_dir: Path, na_decls: dict
) -> tuple[list, list]:
    """检查 specs/{branch}/ 下的必需文件。返回 (failures, skips)。"""
    failures = []
    skips = []
    for f in files:
        filepath = specs_dir / f
        if filepath.exists() and filepath.stat().st_size > 0:
            print(f"  [PASS] {f}")
        elif f in na_decls:
            print(f"  [SKIP] {f} (N/A:{na_decls[f]})")
            skips.append(f)
        else:
            print(f"  [FAIL] {f} — missing or empty")
            failures.append(f)
    return failures, skips


def check_repo_files(
    files: list, repo_root: Path, na_decls: dict
) -> tuple[list, list]:
    """检查 repo 级文件（相对于 repo root）。返回 (failures, skips)。"""
    failures = []
    skips = []
    for f in files:
        filepath = repo_root / f
        if filepath.exists() and filepath.stat().st_size > 0:
            print(f"  [PASS] {f} (repo)")
        elif f in na_decls:
            print(f"  [SKIP] {f} (N/A:{na_decls[f]})")
            skips.append(f)
        else:
            print(f"  [FAIL] {f} — missing or empty (repo)")
            failures.append(f)
    return failures, skips


def check_content(
    checks: list, specs_dir: Path, na_decls: dict
) -> list:
    """执行内容检查（pattern 或 special check）。返回 failures。"""
    failures = []
    for c in checks:
        file = c.get("file", "")
        pattern = c.get("pattern", "")
        special = c.get("check", "")
        filepath = specs_dir / file

        if not filepath.exists():
            if file in na_decls:
                print(f"  [SKIP] {file} content (N/A:{na_decls[file]})")
                continue
            print(f"  [FAIL] {file} — not found for content check")
            failures.append(f"{file}: not found")
            continue

        content = filepath.read_text()

        if special == "no_unchecked_tasks":
            unchecked = len(re.findall(r"^- \[ \]", content, re.MULTILINE))
            if unchecked > 0:
                print(f"  [FAIL] {file} — {unchecked} unchecked tasks")
                failures.append(f"{file}: {unchecked} unchecked tasks")
            else:
                print(f"  [PASS] {file} — all tasks checked")
        elif pattern:
            try:
                if re.search(pattern, content):
                    print(f"  [PASS] {file} — pattern matched: {pattern}")
                else:
                    print(f"  [FAIL] {file} — pattern not found: {pattern}")
                    failures.append(f"{file}: pattern '{pattern}'")
            except re.error as e:
                print(f"  [FAIL] {file} — invalid regex: {pattern} ({e})")
                failures.append(f"{file}: invalid regex '{pattern}'")
    return failures


# 允许执行的命令前缀白名单（防止 YAML 篡改执行任意 shell）
ALLOWED_CMD_PREFIXES = (
    "go ", "cd src && go ", "cd src/ && go ",
    "gocell ", "./gocell ",
    "npx playwright ",
)


def check_commands(commands: list, repo_root: Path) -> list:
    """执行命令检查，验证退出码为 0。返回 failures。"""
    failures = []
    for cmd_spec in commands:
        cmd = cmd_spec.get("cmd", "").strip()
        desc = cmd_spec.get("desc", cmd)
        timeout_sec = cmd_spec.get("timeout", 120)

        # fail-closed: 空命令视为 FAIL
        if not cmd:
            print(f"  [FAIL] {desc} — empty command (misconfigured)")
            failures.append(f"command misconfigured: {desc}")
            continue

        # 白名单校验
        if not any(cmd.startswith(prefix) for prefix in ALLOWED_CMD_PREFIXES):
            print(f"  [FAIL] {desc} — command not in allowlist: {cmd}")
            failures.append(f"command not allowed: {cmd}")
            continue

        try:
            result = subprocess.run(
                cmd, shell=True, cwd=str(repo_root),
                capture_output=True, text=True, timeout=timeout_sec,
            )
            if result.returncode == 0:
                print(f"  [PASS] {desc}")
            else:
                print(f"  [FAIL] {desc} — exit code {result.returncode}")
                if result.stderr.strip():
                    print(f"         {result.stderr.strip()[:200]}")
                failures.append(f"command: {desc}")
        except subprocess.TimeoutExpired:
            print(f"  [FAIL] {desc} — timeout ({timeout_sec}s)")
            failures.append(f"command timeout: {desc}")
        except Exception as e:
            print(f"  [FAIL] {desc} — {e}")
            failures.append(f"command error: {desc}")
    return failures


def main():
    args = parse_args()
    repo_root = find_repo_root()
    gates_file = repo_root / ".claude" / "skills" / "phase-gate" / "phase-gates.yaml"

    data = load_gates(gates_file)
    check_config = get_stage_check(data, args.stage, args.check)

    # fail-closed: 非 S0 entry 的空配置视为 FAIL（防止误配 {} 静默放行）
    if not check_config or not isinstance(check_config, dict):
        if args.stage == "S0" and args.check == "entry":
            check_config = {}  # S0 entry 无条件通过（设计意图）
        else:
            print(f"FAIL — {args.stage} {args.check}: empty or invalid config (fail-closed)")
            sys.exit(1)

    # Validate branch name: prevent path traversal
    if not re.match(r'^[a-zA-Z0-9][a-zA-Z0-9._/-]*$', args.branch) or '..' in args.branch:
        print(f"ERROR: invalid branch name: {args.branch}")
        sys.exit(1)

    # Verify specs_dir stays within repo
    specs_dir = (repo_root / "specs" / args.branch).resolve()
    if not str(specs_dir).startswith(str(repo_root.resolve())):
        print(f"ERROR: branch name escapes repo root: {args.branch}")
        sys.exit(1)

    charter_file = specs_dir / "phase-charter.md"
    audit_log = specs_dir / "gate-audit.log"
    specs_dir.mkdir(parents=True, exist_ok=True)

    na_decls = parse_na_declarations(charter_file)

    print(f"=== Phase Gate Check: {args.stage} / {args.check} ===")
    print(f"Branch: {args.branch}")
    print(f"Specs dir: {specs_dir}")
    print()

    all_failures = []

    # 1. required_files (specs/{branch}/ 下)
    req_files = check_config.get("required_files", [])
    if req_files:
        print("Checking required files (specs/)...")
        failures, _ = check_required_files(req_files, specs_dir, na_decls)
        all_failures.extend(failures)
        print()

    # 2. repo_files (repo root 下)
    repo_files = check_config.get("repo_files", [])
    if repo_files:
        print("Checking repo-level files...")
        failures, _ = check_repo_files(repo_files, repo_root, na_decls)
        all_failures.extend(failures)
        print()

    # 3. content_checks
    content_checks = check_config.get("content_checks", [])
    if content_checks:
        print("Checking content requirements...")
        failures = check_content(content_checks, specs_dir, na_decls)
        all_failures.extend(failures)
        print()

    # 4. command_checks
    cmd_checks = check_config.get("command_checks", [])
    if cmd_checks:
        print("Checking command requirements...")
        failures = check_commands(cmd_checks, repo_root)
        all_failures.extend(failures)
        print()

    # --- Summary ---
    passed = len(all_failures) == 0
    result_str = "PASS" if passed else "FAIL"
    print("=== Result ===")
    if passed:
        print(f"PASS — {args.stage} {args.check} gate satisfied")
    else:
        print(f"FAIL — {args.stage} {args.check} gate NOT satisfied")
        print()
        for f in all_failures:
            print(f"  - {f}")

    # --- Audit log ---
    ts = datetime.now().strftime("%Y-%m-%d %H:%M:%S")
    with open(audit_log, "a") as log:
        log.write(f"[{ts}] {args.stage}/{args.check}: {result_str} (branch={args.branch})\n")
        for f in all_failures:
            log.write(f"  failed: {f}\n")

    print()
    print(f"Audit log: {audit_log}")

    sys.exit(0 if passed else 1)


if __name__ == "__main__":
    main()
