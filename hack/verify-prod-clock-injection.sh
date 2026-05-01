#!/usr/bin/env bash
# verify-prod-clock-injection.sh — invokes the PROD-CLOCK-INJECTION-01
# archtest gate to confirm production code never calls time.Now /
# time.Since / time.Until / time.NewTimer directly. Only kernel/clock
# (which owns the Real implementation) and pkg/securecookie (whose
# depguard-imposed stdlib-only constraint forces a local Clock fallback)
# are exempt.
#
# ref: docs/plans/202605011500-029-master-roadmap.md Track D #D6
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
go test ./tools/archtest/ \
  -run 'TestProdClockInjection' \
  -race -count=1 -timeout 5m -v
