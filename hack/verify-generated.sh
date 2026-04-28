#!/usr/bin/env bash
# verify-generated regenerates checked-in generated artifacts and fails when the
# committed tree is not self-consistent. This mirrors the generated-artifact
# CI gates so local `make verify` catches boundary / metrics-schema drift before
# a PR reaches GitHub Actions.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

# shellcheck source=lib/util.sh
source "${ROOT}/hack/lib/util.sh"

entrypoints_file="$(mktemp)"
trap 'rm -f "${entrypoints_file}"' EXIT

record_generated_entrypoints() {
    local output="$1"
    local path rel
    while IFS= read -r line; do
        [[ "${line}" == Generated:\ * ]] || continue
        path="${line#Generated: }"
        rel="${path#${ROOT}/}"
        case "${rel}" in
            assemblies/*/generated/boundary.yaml) ;;
            *) printf '%s\n' "${rel}" >> "${entrypoints_file}" ;;
        esac
    done <<< "${output}"
}

gocell::log::status "Regenerating assembly entrypoints and boundaries"
for d in assemblies/*/; do
    [[ -d "${d}" ]] || continue
    output="$(go run ./cmd/gocell generate assembly --id "$(basename "${d}")")"
    printf '%s\n' "${output}"
    record_generated_entrypoints "${output}"
done

gocell::log::status "Regenerating metrics schemas"
for d in assemblies/*/; do
    [[ -d "${d}" ]] || continue
    go run ./cmd/gocell generate metrics-schema --id "$(basename "${d}")"
done

failed=0
generated_entrypoints=()
while IFS= read -r entrypoint; do
    [[ -n "${entrypoint}" ]] || continue
    generated_entrypoints+=("${entrypoint}")
done < "${entrypoints_file}"
diff_paths=(assemblies/)
diff_paths+=("${generated_entrypoints[@]}")

if ! git diff --exit-code -- "${diff_paths[@]}"; then
    gocell::log::error "generated artifact drift detected; run 'make generate' and commit the result"
    failed=1
fi

untracked_entrypoints=""
if ((${#generated_entrypoints[@]} > 0)); then
    untracked_entrypoints="$(git ls-files --others --exclude-standard -- "${generated_entrypoints[@]}" 2>/dev/null || true)"
fi
if [[ -n "${untracked_entrypoints}" ]]; then
    gocell::log::error "untracked generated assembly entrypoints found; commit them:"
    printf '%s\n' "${untracked_entrypoints}" >&2
    failed=1
fi

untracked_boundary="$(git ls-files --others --exclude-standard -- 'assemblies/*/generated/boundary.yaml' 2>/dev/null || true)"
if [[ -n "${untracked_boundary}" ]]; then
    gocell::log::error "untracked boundary.yaml files found; commit them:"
    printf '%s\n' "${untracked_boundary}" >&2
    failed=1
fi

untracked_metrics="$(git ls-files --others --exclude-standard -- 'assemblies/*/generated/metrics-schema.yaml' 2>/dev/null || true)"
if [[ -n "${untracked_metrics}" ]]; then
    gocell::log::error "untracked metrics-schema.yaml files found; commit them:"
    printf '%s\n' "${untracked_metrics}" >&2
    failed=1
fi

exit "${failed}"
