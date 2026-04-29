#!/usr/bin/env bash
# PR-CI-6 PROD-DURATION-CONST-01 gate.
# Invariant + scope: see docs/plans/202604272358-2-2-ci-batch2-k8s-verify.md PR-CI-6.
# Implementation: tools/archtest/prod_duration_const_test.go

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

go test ./tools/archtest/ -run TestProdDurationConst -race -count=1 -timeout 2m -v
