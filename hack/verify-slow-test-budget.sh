#!/usr/bin/env bash
# G9 SLOW-TEST-BUDGET — wall-clock budget gate for unit tests.
#
# Pipes `go test -json` from every unit shard through the slowgate binary,
# which fails when any (Package, Test) Elapsed exceeds the threshold (default
# 2s) unless the pair is on tools/slowgate/allowlist.txt.
#
# Companion archtest SLOWGATE-ALLOWLIST-01 (tools/archtest) enforces no-orphan
# entries + preceding `# <reason>` comment per data line.
#
# Invariant + scope: see docs/plans/202605011500-029-master-roadmap.md G9.

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

go run ./tools/slowgate \
  --threshold=2s \
  --allowlist=tools/slowgate/allowlist.txt \
  < <(go test -json -count=1 -timeout 5m \
        ./kernel/... ./tools/... ./runtime/... ./pkg/... ./cells/... \
        ./adapters/... ./cmd/... ./examples/... ./tests/... ./contracts/... \
        2>/dev/null)
