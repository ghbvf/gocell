#!/usr/bin/env bash
# verify-bucket: lint
# pr-handoff-contract-selftest.sh — offline contract checks for the external
# PR app handoff and the /pr-monitor fallback. This does not implement the app;
# it locks the repo-side consumer contract that the app and monitor rely on.
set -euo pipefail

PASS_COUNT=0
FAIL_COUNT=0

count_status_labels() {
    local labels="$1"
    local count=0
    local label
    for label in ${labels}; do
        case "${label}" in
            pr-status/*) count=$((count + 1)) ;;
        esac
    done
    printf '%s' "${count}"
}

has_label() {
    local labels="$1"
    local want="$2"
    local label
    for label in ${labels}; do
        [[ "${label}" == "${want}" ]] && return 0
    done
    return 1
}

route_handoff() {
    local labels="$1"
    local kind="$2"
    local phase="$3"
    local verdict="$4"
    local next_agent="$5"
    local command="$6"
    local trigger_label="$7"
    local same_head="$8"
    local trusted_actor="$9"
    local same_repo="${10}"
    local draft="${11}"
    local prior_failure="${12}"
    local claimed="${13}"

    [[ "$(count_status_labels "${labels}")" == "1" ]] || return 1
    [[ "${trusted_actor}" == "true" ]] || return 1
    [[ "${same_repo}" == "true" ]] || return 1
    [[ "${draft}" == "false" ]] || return 1
    [[ "${prior_failure}" == "false" ]] || return 1
    [[ "${claimed}" == "false" ]] || return 1
    [[ "${same_head}" == "true" ]] || return 1
    has_label "${labels}" "${trigger_label}" || return 1

    case "${kind}|${phase}|${verdict}|${next_agent}|${command}|${trigger_label}" in
        "ship|ship|needs-review-again|codex|codex review|pr-status/needs-review-again")
            printf '%s' "codex review"
            ;;
        "fix|fix|needs-check-fix|claude|/pr-review --check|pr-status/needs-check-fix")
            printf '%s' "/pr-review --check"
            ;;
        "pr-review|review|changes-requested|claude|/fix|pr-status/needs-fix" | \
        "pr-review|check|changes-requested|claude|/fix|pr-status/needs-fix")
            printf '%s' "/fix"
            ;;
        *)
            return 1
            ;;
    esac
}

expect_route() {
    local name="$1"
    local want="$2"
    shift 2
    local got
    if got="$(route_handoff "$@")" && [[ "${got}" == "${want}" ]]; then
        PASS_COUNT=$((PASS_COUNT + 1))
        return
    fi
    FAIL_COUNT=$((FAIL_COUNT + 1))
    printf 'FAIL [%s]: expected route %q, got %q\n' "${name}" "${want}" "${got:-<none>}" >&2
}

expect_deny() {
    local name="$1"
    shift
    if route_handoff "$@" >/dev/null; then
        FAIL_COUNT=$((FAIL_COUNT + 1))
        printf 'FAIL [%s]: expected deny\n' "${name}" >&2
        return
    fi
    PASS_COUNT=$((PASS_COUNT + 1))
}

expect_route "ship-review-handoff" "codex review" \
    "pr-status/needs-review-again" \
    "ship" "ship" "needs-review-again" "codex" "codex review" \
    "pr-status/needs-review-again" "true" "true" "true" "false" "false" "false"

expect_route "fix-check-handoff" "/pr-review --check" \
    "pr-status/needs-check-fix" \
    "fix" "fix" "needs-check-fix" "claude" "/pr-review --check" \
    "pr-status/needs-check-fix" "true" "true" "true" "false" "false" "false"

expect_route "review-fix-handoff" "/fix" \
    "pr-status/needs-fix" \
    "pr-review" "review" "changes-requested" "claude" "/fix" \
    "pr-status/needs-fix" "true" "true" "true" "false" "false" "false"

expect_route "check-fix-handoff" "/fix" \
    "pr-status/needs-fix" \
    "pr-review" "check" "changes-requested" "claude" "/fix" \
    "pr-status/needs-fix" "true" "true" "true" "false" "false" "false"

expect_deny "deny-trigger-label-mismatch" \
    "pr-status/needs-review-again" \
    "pr-review" "review" "changes-requested" "claude" "/fix" \
    "pr-status/needs-fix" "true" "true" "true" "false" "false" "false"

expect_deny "deny-non-review-fix-block" \
    "pr-status/needs-fix" \
    "fix" "fix" "needs-check-fix" "claude" "/pr-review --check" \
    "pr-status/needs-check-fix" "true" "true" "true" "false" "false" "false"

expect_deny "deny-stale-head" \
    "pr-status/needs-fix" \
    "pr-review" "review" "changes-requested" "claude" "/fix" \
    "pr-status/needs-fix" "false" "true" "true" "false" "false" "false"

expect_deny "deny-untrusted-actor" \
    "pr-status/needs-fix" \
    "pr-review" "review" "changes-requested" "claude" "/fix" \
    "pr-status/needs-fix" "true" "false" "true" "false" "false" "false"

expect_deny "deny-fork" \
    "pr-status/needs-fix" \
    "pr-review" "review" "changes-requested" "claude" "/fix" \
    "pr-status/needs-fix" "true" "true" "false" "false" "false" "false"

expect_deny "deny-draft" \
    "pr-status/needs-fix" \
    "pr-review" "review" "changes-requested" "claude" "/fix" \
    "pr-status/needs-fix" "true" "true" "true" "true" "false" "false"

expect_deny "deny-prior-failure" \
    "pr-status/needs-fix" \
    "pr-review" "review" "changes-requested" "claude" "/fix" \
    "pr-status/needs-fix" "true" "true" "true" "false" "true" "false"

expect_deny "deny-idempotency-claimed" \
    "pr-status/needs-fix" \
    "pr-review" "review" "changes-requested" "claude" "/fix" \
    "pr-status/needs-fix" "true" "true" "true" "false" "false" "true"

expect_deny "deny-conflicting-status-labels" \
    "pr-status/needs-fix pr-status/ready" \
    "pr-review" "review" "changes-requested" "claude" "/fix" \
    "pr-status/needs-fix" "true" "true" "true" "false" "false" "false"

if [[ "${FAIL_COUNT}" -ne 0 ]]; then
    printf 'pr-handoff-contract-selftest: %d passed, %d failed\n' "${PASS_COUNT}" "${FAIL_COUNT}" >&2
    exit 1
fi

printf 'pr-handoff-contract-selftest: %d passed, %d failed\n' "${PASS_COUNT}" "${FAIL_COUNT}"
