#!/usr/bin/env bash
set -euo pipefail

# pr-submit.sh — PR 实施完成后的提交流程
# Usage: bash .claude/skills/stage-5-implement/scripts/pr-submit.sh \
#          --branch <pr-branch> --title "<PR title>" --base <target-branch>

BRANCH=""
TITLE=""
BASE=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --branch) BRANCH="$2"; shift 2 ;;
    --title) TITLE="$2"; shift 2 ;;
    --base) BASE="$2"; shift 2 ;;
    *) echo "ERROR: Unknown argument: $1"; exit 1 ;;
  esac
done

if [[ -z "$BRANCH" || -z "$TITLE" || -z "$BASE" ]]; then
  echo "Usage: pr-submit.sh --branch <branch> --title <title> --base <target-branch>"
  exit 1
fi

REPO_ROOT="$(git rev-parse --show-toplevel)"

# 检测 go.mod 位置，兼容主 repo 和 worktree
if [[ -f "$REPO_ROOT/go.mod" ]]; then
  GO_MODULE_ROOT="$REPO_ROOT"
elif [[ -f "$REPO_ROOT/src/go.mod" ]]; then
  GO_MODULE_ROOT="$REPO_ROOT/src"
else
  echo "ERROR: go.mod not found under $REPO_ROOT"
  exit 1
fi

echo "=== PR Submit: $BRANCH ==="

# 1. Build
echo "--- go build ---"
if ! go -C "$GO_MODULE_ROOT" build ./...; then
  echo "FAIL: go build"
  exit 1
fi
echo "[PASS] go build"

# 2. Vet
echo "--- go vet ---"
if ! go -C "$GO_MODULE_ROOT" vet ./...; then
  echo "FAIL: go vet"
  exit 1
fi
echo "[PASS] go vet"

# 3. Test
echo "--- go test ---"
if ! go -C "$GO_MODULE_ROOT" test ./... -count=1; then
  echo "FAIL: go test"
  exit 1
fi
echo "[PASS] go test"

# 4. Commit
echo "--- git commit ---"
echo "--- staged files ---"
git status --short
git add .
if git diff --cached --quiet; then
  echo "WARN: no changes to commit"
else
  git commit -m "feat($BRANCH): $TITLE"
fi

# 5. Push
echo "--- git push ---"
git push -u origin "$BRANCH"

# 6. Create PR
echo "--- gh pr create ---"
PR_URL=$(gh pr create --draft --base "$BASE" --title "$TITLE" --body "Auto-created by pr-submit.sh" 2>&1) || {
  # PR might already exist
  if echo "$PR_URL" | grep -q "already exists"; then
    echo "PR already exists, skipping creation"
    PR_URL=$(gh pr view "$BRANCH" --json url -q .url 2>/dev/null || echo "unknown")
  else
    echo "FAIL: gh pr create: $PR_URL"
    exit 1
  fi
}

echo ""
echo "=== Done ==="
echo "PR: $PR_URL"
echo "Branch: $BRANCH"
