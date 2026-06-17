#!/usr/bin/env bash
# verify-bucket: lint
# verify-automation-selftest.sh — gate the gocell-pr-meta:v1 protocol helper
# (offline engine + emit-block auto-derive glue), the codex-pr-app-dispatcher
# decision logic, the issue-labels backlog guard, and the bucket routing funnel.
# Discovered automatically by make verify (find hack -maxdepth 1 -name 'verify-*.sh').
#
# ref: kubernetes/kubernetes hack/verify-shellcheck.sh — script shape.
set -euo pipefail
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
bash "${REPO_ROOT}/hack/automation/pr-meta.sh" selftest
bash "${REPO_ROOT}/hack/automation/pr-meta-emit-derive-selftest.sh"
bash "${REPO_ROOT}/hack/automation/codex-pr-app-dispatcher/selftest.sh"
bash "${REPO_ROOT}/hack/automation/issue-labels.sh" selftest
bash "${REPO_ROOT}/hack/automation/bucket-coverage-selftest.sh"
bash "${REPO_ROOT}/hack/automation/docs-reconcile-status-selftest.sh"
