#!/usr/bin/env bash
# verify-bucket: lint
# Verifies that kernel/reconcile status docs use the current landed/gate model.

set -euo pipefail

ROOT="${GOCELL_RECONCILE_STATUS_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "${ROOT}"

adr="docs/architecture/202605291600-661-adr-kernel-reconcile-design.md"
spec="docs/plans/specs/202605262359-661-kernel-reconcile-spec.md"
plan="docs/plans/specs/202605262359-661-kernel-reconcile-plan.md"
tasks="docs/plans/specs/202605262359-661-kernel-reconcile-tasks.md"
prd="docs/plans/product-roadmap/202604301030-winmdm-prd-on-gocell.md"

fail=0

require() {
  local pattern="$1"
  local file="$2"
  local message="$3"
  if ! grep -qE "$pattern" "$file"; then
    printf 'verify-docs-reconcile-status: missing %s in %s\n' "$message" "$file" >&2
    fail=1
  fi
}

reject() {
  local pattern="$1"
  local file="$2"
  local message="$3"
  if grep -qE "$pattern" "$file"; then
    printf 'verify-docs-reconcile-status: stale %s in %s\n' "$message" "$file" >&2
    grep -nE "$pattern" "$file" >&2
    fail=1
  fi
}

require 'A1.?A10.*(landed|已.*landed|已.*落地|已.*闭环)' "$adr" "A1-A10 landed status"
require 'runtime/certlifecycle' "$adr" "ADR-1895 runtime/certlifecycle trigger alignment"
require 'ADR-1895' "$adr" "ADR-1895 cross reference"
reject 'docs-only|fencing 设计是 docs|不建代码|尚未合入 develop|develop 上没有任何 .*kernel/reconcile|原型分支 .*661-loop-skeleton' "$adr" "prototype/trunk-not-landed wording"

for file in "$adr" "$spec" "$plan" "$tasks"; do
  reject 'PARKED-ON-TRIGGER' "$file" "PARKED-ON-TRIGGER status"
  reject 'pkicell\.rotation' "$file" "retired pkicell.rotation trigger"
  reject '真消费方[^[:cntrl:]]*pkicell' "$file" "retired pkicell consumer count"
  reject '未落地|trigger gate 封存|不今天建|A6 未落地' "$file" "unlanded/parked historical wording"
done

require 'IMPLEMENTED-HISTORICAL|已落地历史规格' "$spec" "implemented historical spec status"
require 'IMPLEMENTED-HISTORICAL|已落地历史计划' "$plan" "implemented historical plan status"
require 'IMPLEMENTED-HISTORICAL|已落地历史任务' "$tasks" "implemented historical tasks status"

for file in "$spec" "$plan" "$tasks"; do
  require 'ADR-1895' "$file" "ADR-1895 alignment"
  require 'runtime/certlifecycle' "$file" "runtime/certlifecycle alignment"
  require 'row-TTL UPSERT CAS' "$file" "Postgres row-TTL UPSERT CAS leader alignment"
  reject 'pg_try_advisory_lock|PG advisory lock|advisory lock' "$file" "Postgres advisory-lock leader wording"
done

require 'mdmcell/devicelifecycle/zerotrust' "$plan" "current business consumer set"
reject 'trigger 满足时填|661-loop-skeleton|^- \[ \]' "$tasks" "open trigger-era tracking/tasks"

reject 'WSTEP 协议支持（pkicell）' "$prd" "pre-ADR-1895 WSTEP P0 wording"
require '框架证书底座' "$prd" "framework certificate foundation P0 wording"

exit "$fail"
