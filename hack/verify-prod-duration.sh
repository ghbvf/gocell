#!/usr/bin/env bash
# verify-prod-duration.sh — PR-CI-6 PROD-DURATION-CONST-01 gate.
#
# Runs the archtest TestProdDurationConst over the entire repository to ensure
# no production *.go file contains a literal duration argument to time.Sleep /
# time.After / time.NewTimer / time.NewTicker / context.WithTimeout /
# context.WithDeadline / context.AfterFunc.
#
# See docs/plans/202604272358-2-2-ci-batch2-k8s-verify.md PR-CI-6 for
# rationale and exclusion list.

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

go test ./tools/archtest/ -run TestProdDurationConst -count=1 -v
