#!/usr/bin/env bash
# Selftest for hack/verify-docs-reconcile-status.sh.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
SCRIPT="${REPO_ROOT}/hack/verify-docs-reconcile-status.sh"

pass=0
fail=0

make_fixture() {
  local root="$1"
  local mode="$2"

  mkdir -p \
    "${root}/docs/architecture" \
    "${root}/docs/plans/product-roadmap" \
    "${root}/docs/plans/specs"

  cat >"${root}/docs/architecture/202605291600-661-adr-kernel-reconcile-design.md" <<'EOF'
# ADR 661
A1-A10 已 landed/闭环。
ADR-1895 aligns runtime/certlifecycle.
EOF

  cat >"${root}/docs/plans/specs/202605262359-661-kernel-reconcile-spec.md" <<'EOF'
# Spec
IMPLEMENTED-HISTORICAL
ADR-1895 aligns runtime/certlifecycle.
Postgres leader uses row-TTL UPSERT CAS.
EOF

  cat >"${root}/docs/plans/specs/202605262359-661-kernel-reconcile-plan.md" <<'EOF'
# Plan
IMPLEMENTED-HISTORICAL
ADR-1895 aligns runtime/certlifecycle.
Current business consumers: mdmcell/devicelifecycle/zerotrust.
Postgres leader uses row-TTL UPSERT CAS.
EOF

  cat >"${root}/docs/plans/specs/202605262359-661-kernel-reconcile-tasks.md" <<'EOF'
# Tasks
IMPLEMENTED-HISTORICAL
ADR-1895 aligns runtime/certlifecycle.
Postgres leader uses row-TTL UPSERT CAS.
Historical Closure Matrix（A1-A10 已闭环）
EOF

  cat >"${root}/docs/plans/product-roadmap/202604301030-winmdm-prd-on-gocell.md" <<'EOF'
# PRD
框架证书底座
EOF

  case "${mode}" in
    good) ;;
    missing-runtime)
      cat >"${root}/docs/plans/specs/202605262359-661-kernel-reconcile-spec.md" <<'EOF'
# Spec
IMPLEMENTED-HISTORICAL
ADR-1895 only.
Postgres leader uses row-TTL UPSERT CAS.
EOF
      ;;
    parked)
      printf '\nPARKED-ON-TRIGGER\n' >>"${root}/docs/plans/specs/202605262359-661-kernel-reconcile-tasks.md"
      ;;
    advisory)
      printf '\nPostgres leader uses advisory lock.\n' >>"${root}/docs/plans/specs/202605262359-661-kernel-reconcile-plan.md"
      ;;
    stale-consumer)
      printf '\n真消费方 ≥ 4（pkicell/mdmcell/devicelifecycle/zerotrust）\n' >>"${root}/docs/plans/specs/202605262359-661-kernel-reconcile-plan.md"
      ;;
    stale-tracking)
      cat >>"${root}/docs/plans/specs/202605262359-661-kernel-reconcile-tasks.md" <<'EOF'

## Tracking Matrix（trigger 满足时填）
| A3 | `661-loop-skeleton` | `worktrees/661-03-loop` | — | — | — | — |
- [ ] **T99** stale open task
EOF
      ;;
    docs-only-fencing)
      printf '\nfencing 设计是 docs（不建代码）\n' >>"${root}/docs/architecture/202605291600-661-adr-kernel-reconcile-design.md"
      ;;
    old-prd)
      printf '\nWSTEP 协议支持（pkicell）\n' >>"${root}/docs/plans/product-roadmap/202604301030-winmdm-prd-on-gocell.md"
      ;;
    *)
      printf 'unknown fixture mode: %s\n' "${mode}" >&2
      exit 2
      ;;
  esac
}

run_case() {
  local name="$1"
  local mode="$2"
  local expected="$3"
  local tmp
  tmp="$(mktemp -d)"
  make_fixture "${tmp}" "${mode}"

  set +e
  GOCELL_RECONCILE_STATUS_ROOT="${tmp}" bash "${SCRIPT}" >/tmp/docs-reconcile-status-selftest.out 2>&1
  local rc=$?
  set -e
  rm -rf "${tmp}"

  if [[ "${expected}" == "pass" && "${rc}" -eq 0 ]]; then
    printf 'PASS [%s]\n' "${name}"
    pass=$((pass + 1))
    return
  fi
  if [[ "${expected}" == "fail" && "${rc}" -ne 0 ]]; then
    printf 'PASS [%s]\n' "${name}"
    pass=$((pass + 1))
    return
  fi

  printf 'FAIL [%s] expected=%s rc=%s\n' "${name}" "${expected}" "${rc}" >&2
  cat /tmp/docs-reconcile-status-selftest.out >&2
  fail=$((fail + 1))
}

run_case "good-fixture" good pass
run_case "missing-runtime-fails" missing-runtime fail
run_case "parked-status-fails" parked fail
run_case "advisory-lock-fails" advisory fail
run_case "stale-consumer-fails" stale-consumer fail
run_case "stale-tracking-fails" stale-tracking fail
run_case "docs-only-fencing-fails" docs-only-fencing fail
run_case "old-prd-fails" old-prd fail

printf 'docs-reconcile-status selftest: %d passed, %d failed\n' "${pass}" "${fail}"
if [[ "${fail}" -ne 0 ]]; then
  exit 1
fi
