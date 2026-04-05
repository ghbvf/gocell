#!/usr/bin/env bash
set -euo pipefail

# Phase Gate Check Script
# Usage: phase-gate-check.sh --stage S0|S1|...|S8 --branch <branch-name> --check entry|exit
# Reads .claude/skills/phase-gate/phase-gates.yaml and validates required_files + content_checks
# Writes audit log to specs/{branch}/gate-audit.log

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../../../.." && pwd)"
GATES_FILE="$REPO_ROOT/.claude/skills/phase-gate/phase-gates.yaml"

# --- Argument parsing ---
STAGE=""
BRANCH=""
CHECK_TYPE=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --stage) STAGE="$2"; shift 2 ;;
    --branch) BRANCH="$2"; shift 2 ;;
    --check) CHECK_TYPE="$2"; shift 2 ;;
    *) echo "ERROR: Unknown argument: $1"; exit 1 ;;
  esac
done

if [[ -z "$STAGE" || -z "$BRANCH" || -z "$CHECK_TYPE" ]]; then
  echo "Usage: phase-gate-check.sh --stage S0|S1|...|S8 --branch <branch-name> --check entry|exit"
  exit 1
fi

if [[ ! "$STAGE" =~ ^S[0-8]$ ]]; then
  echo "ERROR: --stage must be S0 through S8, got: $STAGE"
  exit 1
fi

if [[ "$CHECK_TYPE" != "entry" && "$CHECK_TYPE" != "exit" ]]; then
  echo "ERROR: --check must be 'entry' or 'exit', got: $CHECK_TYPE"
  exit 1
fi

if [[ ! -f "$GATES_FILE" ]]; then
  echo "ERROR: phase-gates.yaml not found at $GATES_FILE"
  exit 1
fi

# Verify python3 + PyYAML available (required for YAML parsing)
if ! python3 -c "import yaml" 2>/dev/null; then
  echo "ERROR: python3 with PyYAML is required but not available."
  echo "Install: pip3 install pyyaml"
  exit 1
fi

SPECS_DIR="$REPO_ROOT/specs/$BRANCH"
AUDIT_LOG="$SPECS_DIR/gate-audit.log"
CHARTER_FILE="$SPECS_DIR/phase-charter.md"

# Ensure audit log directory exists
mkdir -p "$SPECS_DIR"

TIMESTAMP="$(date '+%Y-%m-%d %H:%M:%S')"
PASS=true
MISSING=()
CONTENT_FAIL=()

echo "=== Phase Gate Check: $STAGE / $CHECK_TYPE ==="
echo "Branch: $BRANCH"
echo "Specs dir: $SPECS_DIR"
echo ""

# --- Extract required_files using python3 ---
REQUIRED_FILES=$(python3 -c "
import yaml, sys
with open('$GATES_FILE') as f:
    data = yaml.safe_load(f)
stage = data.get('stages', {}).get('$STAGE', {})
check = stage.get('$CHECK_TYPE', {})
files = check.get('required_files', [])
for f in files:
    print(f)
" 2>/dev/null || echo "")

# --- Extract content_checks using python3 ---
CONTENT_CHECKS=$(python3 -c "
import yaml, json, sys
with open('$GATES_FILE') as f:
    data = yaml.safe_load(f)
stage = data.get('stages', {}).get('$STAGE', {})
check = stage.get('$CHECK_TYPE', {})
checks = check.get('content_checks', [])
for c in checks:
    print(json.dumps(c))
" 2>/dev/null || echo "")

# --- Check N/A declarations in phase-charter.md ---
is_na_declared() {
  local file="$1"
  if [[ -f "$CHARTER_FILE" ]]; then
    if grep -qi "N/A.*${file}\|${file}.*N/A" "$CHARTER_FILE" 2>/dev/null; then
      return 0
    fi
  fi
  return 1
}

# --- Check required files ---
if [[ -n "$REQUIRED_FILES" ]]; then
  echo "Checking required files..."
  while IFS= read -r reqfile; do
    [[ -z "$reqfile" ]] && continue
    filepath="$SPECS_DIR/$reqfile"
    if [[ -f "$filepath" && -s "$filepath" ]]; then
      echo "  [PASS] $reqfile"
    elif is_na_declared "$reqfile"; then
      echo "  [SKIP] $reqfile (N/A declared in phase-charter.md)"
    else
      echo "  [FAIL] $reqfile — missing or empty"
      MISSING+=("$reqfile")
      PASS=false
    fi
  done <<< "$REQUIRED_FILES"
  echo ""
else
  echo "No required files for $STAGE/$CHECK_TYPE"
  echo ""
fi

# --- Check content_checks ---
if [[ -n "$CONTENT_CHECKS" ]]; then
  echo "Checking content requirements..."
  while IFS= read -r check_json; do
    [[ -z "$check_json" ]] && continue

    file=$(python3 -c "import json; d=json.loads('$check_json'); print(d.get('file',''))" 2>/dev/null)
    pattern=$(python3 -c "import json; d=json.loads('$check_json'); print(d.get('pattern',''))" 2>/dev/null)
    special_check=$(python3 -c "import json; d=json.loads('$check_json'); print(d.get('check',''))" 2>/dev/null)

    filepath="$SPECS_DIR/$file"

    if [[ ! -f "$filepath" ]]; then
      if is_na_declared "$file"; then
        echo "  [SKIP] $file content check (N/A declared)"
        continue
      fi
      echo "  [FAIL] $file — file not found for content check"
      CONTENT_FAIL+=("$file: file not found")
      PASS=false
      continue
    fi

    if [[ "$special_check" == "no_unchecked_tasks" ]]; then
      unchecked=$(grep -c '^\- \[ \]' "$filepath" 2>/dev/null || true)
      if [[ "$unchecked" -gt 0 ]]; then
        echo "  [FAIL] $file — $unchecked unchecked tasks remaining"
        CONTENT_FAIL+=("$file: $unchecked unchecked tasks")
        PASS=false
      else
        echo "  [PASS] $file — all tasks checked"
      fi
    elif [[ -n "$pattern" ]]; then
      if grep -qE "$pattern" "$filepath" 2>/dev/null; then
        echo "  [PASS] $file — pattern matched: $pattern"
      else
        echo "  [FAIL] $file — pattern not found: $pattern"
        CONTENT_FAIL+=("$file: pattern '$pattern' not found")
        PASS=false
      fi
    fi
  done <<< "$CONTENT_CHECKS"
  echo ""
else
  echo "No content checks for $STAGE/$CHECK_TYPE"
  echo ""
fi

# --- Summary ---
echo "=== Result ==="
if $PASS; then
  echo "PASS — $STAGE $CHECK_TYPE gate satisfied"
  RESULT="PASS"
else
  echo "FAIL — $STAGE $CHECK_TYPE gate NOT satisfied"
  RESULT="FAIL"
  if [[ ${#MISSING[@]} -gt 0 ]]; then
    echo ""
    echo "Missing files:"
    for m in "${MISSING[@]}"; do
      echo "  - $m"
    done
  fi
  if [[ ${#CONTENT_FAIL[@]} -gt 0 ]]; then
    echo ""
    echo "Content check failures:"
    for c in "${CONTENT_FAIL[@]}"; do
      echo "  - $c"
    done
  fi
fi

# --- Audit log ---
{
  echo "[$TIMESTAMP] $STAGE/$CHECK_TYPE: $RESULT (branch=$BRANCH)"
  if [[ ${#MISSING[@]} -gt 0 ]]; then
    for m in "${MISSING[@]}"; do
      echo "  missing: $m"
    done
  fi
  if [[ ${#CONTENT_FAIL[@]} -gt 0 ]]; then
    for c in "${CONTENT_FAIL[@]}"; do
      echo "  content_fail: $c"
    done
  fi
} >> "$AUDIT_LOG"

echo ""
echo "Audit log written to: $AUDIT_LOG"

if $PASS; then
  exit 0
else
  exit 1
fi
