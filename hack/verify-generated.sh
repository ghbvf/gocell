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

gocell::log::status "Regenerating assembly boundaries"
for d in assemblies/*/; do
    [[ -d "${d}" ]] || continue
    go run ./cmd/gocell generate assembly --id "$(basename "${d}")" --boundary-only
done

gocell::log::status "Regenerating metrics schemas"
for d in assemblies/*/; do
    [[ -d "${d}" ]] || continue
    go run ./cmd/gocell generate metrics-schema --id "$(basename "${d}")"
done

failed=0

if ! git diff --exit-code -- assemblies/; then
    gocell::log::error "generated artifact drift detected; run 'make generate' and commit the result"
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
