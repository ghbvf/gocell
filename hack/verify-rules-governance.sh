#!/usr/bin/env bash
# Verifies that agent instruction rules stay short and future-facing.
# `make verify` discovers this gate automatically.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

go test ./tools/archtest -run '^TestAgentRulesGovernance$' -count=1 -timeout 30s
