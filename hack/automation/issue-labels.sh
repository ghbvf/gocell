#!/usr/bin/env bash
# issue-labels.sh — guard backlog-issue label completeness (#1832).
#
# Closes the Cx labeling loop. A non-epic backlog issue must carry exactly one
# each of area-* / type-* / pri-* / cx-* — cx is now MANDATORY, symmetric to pri
# (PROJECT.md §2.6). An epic backlog issue requires area-*/pri-* and must NOT
# carry any cx-* (epic 不贴 cx — PROJECT.md :23). Non-backlog issues are out of
# scope (return ok).
#
# INVARIANT: non-epic backlog ⇒ exactly-one {area,type,pri,cx}; epic ⇒
# exactly-one {area,pri} ∧ zero cx. Enforcement is a skill-side pre-create gate
# (issues B1 runs `validate --labels` and only `gh issue create`s on exit 0).
# Funnel strength (honest, per .claude/rules/gocell/ai-robust.md): overall Medium.
# Downstream logic Medium (single decision point; selftest is the golden gate, run
# in CI via make verify — not compile-time un-expressible, so not Hard). Upstream
# callsite weak-Medium (skill-routed; a raw `gh` / web-UI create bypasses it). True
# Hard (违反不可表达) is structurally unreachable — GitHub issue labels are external
# mutable state no repo-side mechanism can constrain. The unconditional `on: issues`
# CI backstop (the strong-Medium ceiling) is a deliberate future-hardening path,
# not built here.
#
# Usage:
#   issue-labels.sh validate --labels "<csv>"   pure offline validator
#   issue-labels.sh validate --issue <N>        validate a live issue (needs gh)
#   issue-labels.sh selftest                    offline red/green regression
# Exit: 0 ok | 1 gh/IO error | 2 validation violation | 64 usage error
#
# ref: hack/automation/pr-meta.sh — subcommand dispatch + embedded selftest shape.

set -euo pipefail

# ---- core -------------------------------------------------------------------

# _normalize CSV -> newline-separated, trimmed, blank-stripped label list.
_normalize() {
    printf '%s' "$1" \
        | tr ',' '\n' \
        | sed 's/^[[:space:]]*//;s/[[:space:]]*$//' \
        | grep -v '^[[:space:]]*$' || true
}

# _count REGEX LABELS -> number of labels matching REGEX (anchored by caller).
_count() {
    printf '%s\n' "$2" | grep -cE "$1" || true
}

# _require_one NAME COUNT -> 0 ok; prints + returns 1 on missing/conflict.
_require_one() {
    local name="$1" count="$2"
    if [[ "${count}" -eq 0 ]]; then
        echo "  - missing ${name} label (exactly 1 required)" >&2
        return 1
    fi
    if [[ "${count}" -gt 1 ]]; then
        echo "  - conflict: ${count} ${name} labels (exactly 1 required)" >&2
        return 1
    fi
    return 0
}

# _validate_labels CSV -> 0 ok | 2 violation. Prints violations to stderr.
#
# Axis membership: pri/cx use value-exact regexes (pri-p0..p3, cx-1..cx-4) — these
# axes have small fixed value sets and exactness is load-bearing: it rejects
# cx-unknown (deliberately unsupported, #1832) and out-of-range typos. area/type use
# prefix-count: their value sets (PROJECT.md §2.1/§2.2, 8 each) are validated by
# GitHub label existence + the enum docs, not duplicated here — this guard checks
# COMPLETENESS (exactly-one per axis), not value membership.
_validate_labels() {
    local labels
    labels="$(_normalize "$1")"

    # Out of scope: only backlog issues are governed.
    printf '%s\n' "${labels}" | grep -qx 'backlog' || return 0

    local area type pri cx cx_any is_epic
    area="$(_count '^area-' "${labels}")"
    type="$(_count '^type-' "${labels}")"
    pri="$(_count '^pri-p[0-3]$' "${labels}")"
    cx="$(_count '^cx-[1-4]$' "${labels}")"
    cx_any="$(_count '^cx-' "${labels}")"
    is_epic="$(_count '^epic$' "${labels}")"

    local rc=0
    _require_one 'area' "${area}" || rc=1
    _require_one 'pri (pri-p0..p3)' "${pri}" || rc=1
    if [[ "${is_epic}" -gt 0 ]]; then
        # epic 不贴 cx; type not required for epic (epic create cmd omits it).
        # cx_any (not cx) so a cx-unknown / typo on an epic is also rejected.
        if [[ "${cx_any}" -gt 0 ]]; then
            echo "  - epic must not carry cx-* (epic 不贴 cx)" >&2
            rc=1
        fi
    else
        _require_one 'type' "${type}" || rc=1
        _require_one 'cx (cx-1..cx-4)' "${cx}" || rc=1
    fi

    if [[ "${rc}" -ne 0 ]]; then
        echo "issue-labels: backlog label set incomplete: $1" >&2
        return 2
    fi
    return 0
}

# ---- subcommands ------------------------------------------------------------

cmd_validate() {
    local labels="" issue="" have_labels=0
    while [[ $# -gt 0 ]]; do
        case "$1" in
            --labels)   [[ $# -ge 2 ]] || { echo "issue-labels validate: --labels requires a value" >&2; return 64; }
                        labels="$2"; have_labels=1; shift 2 ;;
            --labels=*) labels="${1#*=}"; have_labels=1; shift ;;
            --issue)    [[ $# -ge 2 ]] || { echo "issue-labels validate: --issue requires a value" >&2; return 64; }
                        issue="$2"; shift 2 ;;
            --issue=*)  issue="${1#*=}"; shift ;;
            *) echo "issue-labels validate: unknown flag '$1'" >&2; return 64 ;;
        esac
    done

    if [[ -n "${issue}" ]]; then
        command -v gh >/dev/null 2>&1 || { echo "issue-labels: gh not found" >&2; return 1; }
        labels="$(gh issue view "${issue}" --json labels -q '[.labels[].name] | join(",")' 2>/dev/null)" \
            || { echo "issue-labels: gh issue view ${issue} failed" >&2; return 1; }
    elif [[ "${have_labels}" -eq 0 ]]; then
        echo "issue-labels validate: --labels or --issue required" >&2
        return 64
    fi

    local rc=0
    _validate_labels "${labels}" || rc=$?
    return "${rc}"
}

cmd_selftest() {
    local pass=0 fail=0
    _expect() {  # NAME WANT_RC -- validate-args...
        local name="$1" want="$2"; shift 2
        local got=0
        cmd_validate "$@" >/dev/null 2>&1 || got=$?
        if [[ "${got}" -eq "${want}" ]]; then
            echo "PASS [${name}]"; pass=$((pass + 1))
        else
            echo "FAIL [${name}]: want rc=${want} got rc=${got}"; fail=$((fail + 1))
        fi
    }

    # non-epic backlog, all four axes present -> ok
    _expect "complete"      0 --labels "backlog,area-tooling,type-debt,pri-p2,cx-1"
    # the #1829/#1830 case: cx omitted -> violation
    _expect "missing-cx"    2 --labels "backlog,area-tooling,type-debt,pri-p2"
    _expect "missing-pri"   2 --labels "backlog,area-tooling,type-debt,cx-1"
    _expect "missing-area"  2 --labels "backlog,type-debt,pri-p2,cx-1"
    _expect "missing-type"  2 --labels "backlog,area-tooling,pri-p2,cx-1"
    # two cx labels -> conflict
    _expect "double-cx"     2 --labels "backlog,area-tooling,type-debt,pri-p2,cx-1,cx-2"
    # epic backlog: area+pri, no cx, no type required -> ok
    _expect "epic-ok"       0 --labels "epic,backlog,area-tooling,pri-p2"
    # epic must not carry cx
    _expect "epic-with-cx"  2 --labels "epic,backlog,area-tooling,pri-p2,cx-1"
    # not a backlog issue -> out of scope, ok
    _expect "non-backlog"   0 --labels "area-tooling,type-debt,pri-p2"
    # tolerates whitespace + orthogonal flag labels
    _expect "ws-and-flag"   0 --labels "backlog, area-tooling , type-debt, pri-p2, cx-3, flag-cond"
    # empty set -> out of scope (no backlog) — locks _normalize / out-of-scope path
    _expect "empty-labels"  0 --labels ""
    # cx-unknown is deliberately unsupported (#1832): not a valid cx -> violation
    _expect "cx-unknown"    2 --labels "backlog,area-tooling,type-debt,pri-p2,cx-unknown"
    # out-of-range cx (Cx5+ must be split, §3.2) is not a valid cx -> violation
    _expect "cx-out-of-range" 2 --labels "backlog,area-tooling,type-debt,pri-p2,cx-5"
    # invalid pri value (only pri-p0..p3) -> violation
    _expect "pri-invalid"   2 --labels "backlog,area-tooling,type-debt,pri-p10,cx-1"
    # epic carrying cx-unknown still rejected (epic forbid uses cx_any, not cx-1..4)
    _expect "epic-cx-unknown" 2 --labels "epic,backlog,area-tooling,pri-p2,cx-unknown"
    # usage errors: missing flag value / no flag at all -> 64
    _expect "labels-no-value" 64 --labels
    _expect "issue-no-value"  64 --issue
    _expect "no-flag"       64

    echo "issue-labels selftest: ${pass} passed, ${fail} failed"
    [[ "${fail}" -eq 0 ]]
}

usage() {
    cat >&2 <<'EOF'
usage: issue-labels.sh <validate|selftest> [args]
  validate --labels "<csv>"   validate an explicit label set (offline)
  validate --issue <N>        validate a live issue's labels (needs gh)
  selftest                    run offline red/green regression
exit codes: 0 ok | 1 gh/IO error | 2 validation violation | 64 usage error
EOF
}

main() {
    local sub="${1:-}"
    if [[ $# -gt 0 ]]; then shift; fi
    case "${sub}" in
        validate) cmd_validate "$@" ;;
        selftest) cmd_selftest "$@" ;;
        -h|--help|help) usage; exit 0 ;;
        "") usage; exit 64 ;;
        *) echo "issue-labels: unknown subcommand '${sub}'" >&2; usage; exit 64 ;;
    esac
}

main "$@"
