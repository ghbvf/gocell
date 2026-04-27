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

declare -a results=()
fails=()
for script in "${scripts[@]}"; do
    name="$(basename "${script}")"
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
            if [[ "${status}" == "PASS" ]]; then
                echo "| \`${gate}\` | ✅ PASS |"
            else
                echo "| \`${gate}\` | ❌ FAIL |"
            fi
        done
    } >> "${GITHUB_STEP_SUMMARY}"
fi

if [[ ${#fails[@]} -gt 0 ]]; then
    gocell::log::error "verify failures (${#fails[@]}):"
    printf '  - %s\n' "${fails[@]}" >&2
    exit 1
fi

gocell::log::status "All ${#scripts[@]} verify gates passed."
