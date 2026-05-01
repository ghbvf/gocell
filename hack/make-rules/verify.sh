#!/usr/bin/env bash
# verify.sh runs every hack/verify-*.sh script in deterministic order and
# accumulates failures. Mirrors Kubernetes' hack/make-rules/verify.sh: a glob
# discovery model so adding a new gate only needs a new hack/verify-X.sh file,
# never a change to this driver.
#
# ref: kubernetes/kubernetes hack/make-rules/verify.sh

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT}"

# shellcheck source=../lib/util.sh
source "${ROOT}/hack/lib/util.sh"

scripts=()
while IFS= read -r f; do
    scripts+=("${f}")
done < <(find hack -maxdepth 1 -name 'verify-*.sh' -type f | sort)

if [[ ${#scripts[@]} -eq 0 ]]; then
    gocell::log::error "no hack/verify-*.sh scripts found"
    exit 1
fi

# VERIFY_SKIP is a comma-separated list of gate basenames (the part between
# `verify-` and `.sh`) to skip. CI uses it to drop gates already covered by
# the build-test matrix — running `go test -race ./tools/archtest/...` twice
# on every PR (once in build-test (tools), once here) is pure duplicate
# work. Local `make verify` leaves VERIFY_SKIP empty for full coverage.
#
# Stored as a delimited string instead of an associative array so the script
# stays portable to macOS bash 3.2 (no `declare -A`).
skip_list="|"
if [[ -n "${VERIFY_SKIP:-}" ]]; then
    IFS=',' read -ra entries <<< "${VERIFY_SKIP}"
    for entry in "${entries[@]}"; do
        entry="${entry//[[:space:]]/}"
        if [[ -z "${entry}" ]]; then
            continue
        fi
        # Reject entries that contain anything outside [a-zA-Z0-9_-]. Without
        # this guard a value like `archtest|verify-other` would inject a
        # second pipe-delimited token into skip_list and silently skip an
        # unrelated gate.
        if ! [[ "${entry}" =~ ^[a-zA-Z0-9_-]+$ ]]; then
            gocell::log::error "VERIFY_SKIP entry '${entry}' contains invalid characters (allowed: [a-zA-Z0-9_-])"
            exit 1
        fi
        skip_list+="verify-${entry}.sh|"
    done
fi

declare -a results=()
fails=()
ran=0
for script in "${scripts[@]}"; do
    name="$(basename "${script}")"
    if [[ "${skip_list}" == *"|${name}|"* ]]; then
        results+=("${name}|SKIP")
        gocell::log::status "SKIP: ${name} (VERIFY_SKIP)"
        continue
    fi
    ran=$((ran + 1))
    gocell::log::status "Running ${name}"
    if ! bash "${script}"; then
        fails+=("${name}")
        results+=("${name}|FAIL")
        gocell::log::status "FAIL: ${name}"
    else
        results+=("${name}|PASS")
        gocell::log::status "PASS: ${name}"
    fi
done

# When invoked under GitHub Actions, write a per-gate status table to the job
# summary so reviewers can see which gate failed without expanding the step
# log. Mirrors the gate-level summary that Kubernetes emits via juLog/JUnit.
if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
    {
        echo "## make verify gates"
        echo
        echo "| Gate | Status |"
        echo "| --- | --- |"
        for entry in "${results[@]}"; do
            gate="${entry%|*}"
            status="${entry##*|}"
            case "${status}" in
                PASS) echo "| \`${gate}\` | ✅ PASS |" ;;
                SKIP) echo "| \`${gate}\` | ⏭️ SKIP |" ;;
                *)    echo "| \`${gate}\` | ❌ FAIL |" ;;
            esac
        done
    } >> "${GITHUB_STEP_SUMMARY}"
fi

if [[ ${#fails[@]} -gt 0 ]]; then
    gocell::log::error "verify failures (${#fails[@]}):"
    printf '  - %s\n' "${fails[@]}" >&2
    exit 1
fi

skipped=$(( ${#scripts[@]} - ran ))
if [[ ${ran} -eq 0 ]]; then
    gocell::log::error "all ${#scripts[@]} verify gates were skipped — VERIFY_SKIP is too broad"
    exit 1
fi
if [[ ${skipped} -gt 0 ]]; then
    gocell::log::status "All ${ran} verify gates passed (${skipped} skipped via VERIFY_SKIP)."
else
    gocell::log::status "All ${ran} verify gates passed."
fi
