#!/usr/bin/env bash
# verify-bucket: lint
# verify-bucket-coverage.sh — assert every hack/verify-*.sh gate declares
# exactly one valid `# verify-bucket: <name>` annotation.
#
# This is the anti-vacuity backbone of the CI parallel-bucket model: the
# governance.yml matrix is DERIVED from these annotations (generate-buckets job),
# so a gate with no — or a malformed / duplicated — annotation would be silently
# dropped from every parallel leg and never run on a PR. Here that is a hard red
# instead. (AC#4 of #1817: 漏跑某 gate → 硬红.)
#
# Scope of this guard, honestly: it guarantees full ROUTING coverage only. It
# does NOT bound per-bucket wall-clock — cost-balance across buckets is a
# measured, manual decision (the cost 闸门 was consciously descoped for dev
# velocity; see ADR 202606-1817-adr-governance-lane-parallelization §AC#3). A
# new gate must declare a bucket, but nothing here caps how slow that bucket may
# grow.
#
# Single source for reading the annotation: hack/lib/buckets.sh.
# Regression selftest with synthetic red fixtures (missing / malformed /
# duplicate annotation, empty-dir anti-vacuity):
#   hack/automation/bucket-coverage-selftest.sh  (run by verify-automation-selftest.sh).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

# shellcheck source=lib/util.sh
source "${ROOT}/hack/lib/util.sh"
# shellcheck source=lib/buckets.sh
source "${ROOT}/hack/lib/buckets.sh"

# GOCELL_VERIFY_HACK_DIR aims the scan at a synthetic fixture dir (selftest
# only); production always scans hack/.
hack_dir="${GOCELL_VERIFY_HACK_DIR:-hack}"

gates=()
while IFS= read -r f; do
    gates+=("${f}")
done < <(find "${hack_dir}" -maxdepth 1 -name 'verify-*.sh' -type f | sort)

# anti-vacuity #1: an empty discovery set would make every per-gate assertion
# vacuously true. A real hack/ always has at least this guard plus its peers.
if [[ ${#gates[@]} -eq 0 ]]; then
    gocell::log::error "no verify-*.sh gates discovered in ${hack_dir} — bucket coverage scan is vacuous"
    exit 1
fi

fails=0
saw_self=0
for f in "${gates[@]}"; do
    name="$(basename "${f}")"
    [[ "${name}" == "verify-bucket-coverage.sh" ]] && saw_self=1
    count="$(grep -cE '^# verify-bucket:' "${f}" 2>/dev/null || true)"
    if [[ "${count}" -eq 0 ]]; then
        gocell::log::error "${name}: missing '# verify-bucket: <name>' annotation"
        fails=$((fails + 1))
        continue
    fi
    if [[ "${count}" -gt 1 ]]; then
        gocell::log::error "${name}: ${count} '# verify-bucket:' lines — declare exactly one"
        fails=$((fails + 1))
        continue
    fi
    # Exactly one line present but the lenient reader rejected it → malformed value.
    if [[ -z "$(gocell::buckets::annotation "${f}")" ]]; then
        raw="$(grep -E '^# verify-bucket:' "${f}" | head -1)"
        gocell::log::error "${name}: malformed bucket annotation (need '# verify-bucket: <name>', name = ^[a-z][a-z0-9-]*\$): ${raw}"
        fails=$((fails + 1))
        continue
    fi
done

# anti-vacuity #2: the scan must include this very guard. If the glob ever stops
# matching verify-bucket-coverage.sh (renamed / moved out of hack/), the
# coverage promise is hollow — every gate could be unannotated and the loop
# above would still have run, so anchor on observing self.
if [[ ${saw_self} -eq 0 ]]; then
    gocell::log::error "bucket-coverage scan did not include verify-bucket-coverage.sh — discovery is broken"
    exit 1
fi

if [[ ${fails} -gt 0 ]]; then
    gocell::log::error "bucket coverage: ${fails} gate(s) without a valid '# verify-bucket' annotation"
    exit 1
fi

gocell::log::status "bucket coverage: all ${#gates[@]} gates declare a valid '# verify-bucket'"
