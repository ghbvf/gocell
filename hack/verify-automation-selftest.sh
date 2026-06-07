#!/usr/bin/env bash
# verify-automation-selftest.sh — gate the gocell-pr-meta:v1 protocol helper.
# Discovered automatically by make verify (find hack -maxdepth 1 -name 'verify-*.sh').
#
# ref: kubernetes/kubernetes hack/verify-shellcheck.sh — script shape.
set -euo pipefail
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
bash "${REPO_ROOT}/hack/automation/pr-meta.sh" selftest
