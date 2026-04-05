#!/usr/bin/env bash
set -euo pipefail

# run-qa.sh — S7 自动化测试套件
# Usage: bash scripts/run-qa.sh --branch <branch-name>

BRANCH=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --branch) BRANCH="$2"; shift 2 ;;
    *) echo "ERROR: Unknown argument: $1"; exit 1 ;;
  esac
done

if [[ -z "$BRANCH" ]]; then
  echo "Usage: bash scripts/run-qa.sh --branch <branch-name>"
  exit 1
fi

# scripts/ -> stage-7-qa/ -> skills/ -> .claude/ -> repo_root
REPO_ROOT="$(cd "$(dirname "$0")/../../../.." && pwd)"
EVIDENCE_DIR="$REPO_ROOT/specs/$BRANCH/evidence"
PASS=true
SUMMARY=()

# 创建证据目录
mkdir -p "$EVIDENCE_DIR"/{go-test,validate,journey,playwright}

echo "=== GoCell QA Test Suite ==="
echo "Branch: $BRANCH"
echo "Evidence: $EVIDENCE_DIR"
echo ""

# 1. Go test
echo "--- Running go test ---"
if (cd "$REPO_ROOT/src" && go test ./... -v -count=1) > "$EVIDENCE_DIR/go-test/result.txt" 2>&1; then
  SUMMARY+=("[PASS] go test")
  echo "[PASS] go test"
else
  SUMMARY+=("[FAIL] go test")
  echo "[FAIL] go test — see evidence/go-test/result.txt"
  PASS=false
fi

# 2. go vet
echo "--- Running go vet ---"
if (cd "$REPO_ROOT/src" && go vet ./...) > "$EVIDENCE_DIR/go-test/vet.txt" 2>&1; then
  SUMMARY+=("[PASS] go vet")
  echo "[PASS] go vet"
else
  SUMMARY+=("[FAIL] go vet")
  echo "[FAIL] go vet — see evidence/go-test/vet.txt"
  PASS=false
fi

# 3. gocell validate（如果存在）
echo "--- Running gocell validate ---"
if command -v gocell &>/dev/null; then
  if gocell validate > "$EVIDENCE_DIR/validate/result.txt" 2>&1; then
    SUMMARY+=("[PASS] gocell validate")
    echo "[PASS] gocell validate"
  else
    SUMMARY+=("[FAIL] gocell validate")
    echo "[FAIL] gocell validate — see evidence/validate/result.txt"
    PASS=false
  fi
else
  echo "[SKIP] gocell not installed"
  echo "gocell binary not found — skipped" > "$EVIDENCE_DIR/validate/result.txt"
  SUMMARY+=("[SKIP] gocell validate")
fi

# 4. Journey tests（如果有 J-*.yaml）
echo "--- Running journey tests ---"
JOURNEY_COUNT=0
JOURNEY_FAIL=0
for jfile in "$REPO_ROOT"/src/journeys/J-*.yaml; do
  [[ -f "$jfile" ]] || continue
  jid=$(basename "$jfile" .yaml)
  JOURNEY_COUNT=$((JOURNEY_COUNT + 1))
  if command -v gocell &>/dev/null; then
    if gocell verify journey --id="$jid" > "$EVIDENCE_DIR/journey/$jid.txt" 2>&1; then
      echo "  [PASS] $jid"
    else
      echo "  [FAIL] $jid"
      JOURNEY_FAIL=$((JOURNEY_FAIL + 1))
    fi
  else
    echo "gocell not installed — skipped" > "$EVIDENCE_DIR/journey/$jid.txt"
  fi
done
if [[ $JOURNEY_COUNT -eq 0 ]]; then
  SUMMARY+=("[SKIP] journey tests (no J-*.yaml)")
elif [[ $JOURNEY_FAIL -gt 0 ]]; then
  SUMMARY+=("[FAIL] journey tests ($JOURNEY_FAIL/$JOURNEY_COUNT failed)")
  PASS=false
else
  SUMMARY+=("[PASS] journey tests ($JOURNEY_COUNT passed)")
fi

# 5. Summary
echo ""
echo "=== QA Summary ==="
for s in "${SUMMARY[@]}"; do echo "  $s"; done
echo ""

if $PASS; then
  echo "RESULT: ALL PASS"
  exit 0
else
  echo "RESULT: SOME FAILED"
  exit 1
fi
