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

fails=()
for script in "${scripts[@]}"; do
    name="$(basename "${script}")"
    gocell::log::status "Running ${name}"
    if ! bash "${script}"; then
        fails+=("${name}")
        gocell::log::status "FAIL: ${name}"
    else
        gocell::log::status "PASS: ${name}"
    fi
done

if [[ ${#fails[@]} -gt 0 ]]; then
    gocell::log::error "verify failures (${#fails[@]}):"
    printf '  - %s\n' "${fails[@]}" >&2
    exit 1
fi

gocell::log::status "All ${#scripts[@]} verify gates passed."
