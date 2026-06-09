#!/usr/bin/env bash
# Generate one merged coverage profile across every go.work member module.

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

# shellcheck source=lib/util.sh
source hack/lib/util.sh
# shellcheck source=lib/modules.sh
source hack/lib/modules.sh

if ! modules_raw="$(gocell::modules::dirs)"; then
    gocell::log::error "workspace module enumeration failed"
    exit 1
fi

tmp_dir="$(mktemp -d)"
trap 'rm -rf "${tmp_dir}"' EXIT

out="coverage.out"
printf 'mode: count\n' > "${out}"
covered=0

while IFS= read -r dir; do
    [[ -n "${dir}" ]] || continue
    safe_dir="${dir//[^A-Za-z0-9_.-]/_}"
    profile="${tmp_dir}/${safe_dir}.out"
    gocell::log::status "Covering module: ${dir}"
    if ! go -C "${dir}" test ./... -covermode=count -coverprofile="${profile}"; then
        gocell::log::error "go test -cover failed in module '${dir}'"
        exit 1
    fi
    if [[ -f "${profile}" ]] && [[ "$(wc -l < "${profile}")" -gt 1 ]]; then
        tail -n +2 "${profile}" >> "${out}"
        covered=1
    fi
done <<< "${modules_raw}"

if [[ "${covered}" -eq 0 ]]; then
    gocell::log::error "no coverage data was produced by workspace modules"
    exit 1
fi

go tool cover -func="${out}" | tail -1
