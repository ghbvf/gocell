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

# Local convenience uses `go run` (compiles each invocation, no binary
# artifact left on disk). CI builds the binary once via `go build -o
# $RUNNER_TEMP/slowgate ./tools/slowgate` and reuses it across the 5 unit
# shards. Local elapsed timings on a busy laptop are typically ~10-30%
# higher than the GHA ubuntu-latest runner; if a borderline test fires
# here that doesn't fire in CI, prefer adjusting the test rather than
# adding it to the allowlist on the basis of a local laptop measurement.
go run ./tools/slowgate \
  --threshold=2s \
  --allowlist=tools/slowgate/allowlist.txt \
  < <(go test -json -count=1 -timeout 5m \
        ./kernel/... ./tools/... ./runtime/... ./pkg/... ./cells/... \
        ./adapters/... ./cmd/... ./examples/... ./tests/... ./contracts/... \
        2>/dev/null)
