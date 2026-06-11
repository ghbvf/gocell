#!/usr/bin/env bash
# bucket-coverage-selftest.sh — offline regression selftest for the
# # verify-bucket routing funnel: the verify-bucket-coverage.sh guard and the
# hack/make-rules/verify.sh VERIFY_BUCKET driver routing.
#
# Both consume hack/lib/buckets.sh as the single source for reading a gate's
# `# verify-bucket: <name>` annotation. This selftest builds SYNTHETIC hack
# dirs (via GOCELL_VERIFY_HACK_DIR) so the guard + driver logic is exercised
# without running any real gate. The real-repo enforcement is owned by the
# verify-bucket-coverage.sh gate itself (runs in its bucket on every PR).
#
# Usage:  bash hack/automation/bucket-coverage-selftest.sh
# Exit:   0 = all checks PASS; non-zero = at least one FAIL (or anti-vacuity)
#
# ref: hack/automation/codex-pr-router/router-selftest.sh — harness shape.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd -P)"
GUARD="${REPO_ROOT}/hack/verify-bucket-coverage.sh"
DRIVER="${REPO_ROOT}/hack/make-rules/verify.sh"

# ---------------------------------------------------------------------------
# Harness
# ---------------------------------------------------------------------------

PASS_COUNT=0
FAIL_COUNT=0
CHECK_COUNT=0
# EXPECTED_CHECKS is the anti-vacuity anchor: if the script returns early or a
# scenario is silently skipped, the final count won't match and the selftest
# fails — the same false-green defence router-selftest.sh uses.
EXPECTED_CHECKS=11

pass() { echo "PASS [$1]"; PASS_COUNT=$(( PASS_COUNT + 1 )); CHECK_COUNT=$(( CHECK_COUNT + 1 )); }
fail() { echo "FAIL [$1]: $2"; FAIL_COUNT=$(( FAIL_COUNT + 1 )); CHECK_COUNT=$(( CHECK_COUNT + 1 )); }

# assert_ok <name> — run the command (already captured); pass iff exit 0.
assert_exit() {
    local name="$1" want="$2" got="$3"
    if [[ "${got}" -eq "${want}" ]]; then
        pass "${name}"
    else
        fail "${name}" "expected exit ${want}, got ${got}"
    fi
}

assert_contains() {
    local name="$1" haystack="$2" needle="$3"
    if printf '%s' "${haystack}" | grep -qF "${needle}"; then
        pass "${name}"
    else
        fail "${name}" "expected output to contain '${needle}'; got: ${haystack}"
    fi
}

assert_not_contains() {
    local name="$1" haystack="$2" needle="$3"
    if printf '%s' "${haystack}" | grep -qF "${needle}"; then
        fail "${name}" "expected output NOT to contain '${needle}'; got: ${haystack}"
    else
        pass "${name}"
    fi
}

WORKDIR="$(mktemp -d)"
cleanup() { rm -rf "${WORKDIR}"; }
trap cleanup EXIT

# make_gate <dir> <name> [bucket-line...] — write a minimal fake gate. Any
# extra args are emitted verbatim as header lines (used to inject the
# `# verify-bucket:` annotation, omit it, duplicate it, or malform it).
make_gate() {
    local dir="$1" name="$2"; shift 2
    {
        echo '#!/usr/bin/env bash'
        local line
        for line in "$@"; do printf '%s\n' "${line}"; done
        echo 'exit 0'
    } > "${dir}/${name}"
}

# ---------------------------------------------------------------------------
# Scenario A — guard: a fully-annotated fixture passes.
# ---------------------------------------------------------------------------
A="${WORKDIR}/A"; mkdir -p "${A}"
make_gate "${A}" verify-alpha.sh '# verify-bucket: inv'
make_gate "${A}" verify-beta.sh '# verify-bucket: lint'
make_gate "${A}" verify-bucket-coverage.sh '# verify-bucket: lint'   # the guard self-includes
set +e
out="$(GOCELL_VERIFY_HACK_DIR="${A}" bash "${GUARD}" 2>&1)"; rc=$?
set -e
assert_exit "guard/all-annotated exits 0" 0 "${rc}"

# ---------------------------------------------------------------------------
# Scenario B — guard: a gate missing the annotation is a hard fail.
# ---------------------------------------------------------------------------
B="${WORKDIR}/B"; mkdir -p "${B}"
make_gate "${B}" verify-alpha.sh '# verify-bucket: inv'
make_gate "${B}" verify-orphan.sh        # no annotation
make_gate "${B}" verify-bucket-coverage.sh '# verify-bucket: lint'
set +e
out="$(GOCELL_VERIFY_HACK_DIR="${B}" bash "${GUARD}" 2>&1)"; rc=$?
set -e
assert_exit "guard/missing-annotation fails" 1 "${rc}"
assert_contains "guard/missing names the orphan gate" "${out}" "verify-orphan.sh"

# ---------------------------------------------------------------------------
# Scenario C — guard: a malformed bucket value is a hard fail.
# ---------------------------------------------------------------------------
C="${WORKDIR}/C"; mkdir -p "${C}"
make_gate "${C}" verify-alpha.sh '# verify-bucket: Inv_Bad'   # uppercase + underscore: illegal
make_gate "${C}" verify-bucket-coverage.sh '# verify-bucket: lint'
set +e
out="$(GOCELL_VERIFY_HACK_DIR="${C}" bash "${GUARD}" 2>&1)"; rc=$?
set -e
assert_exit "guard/malformed-value fails" 1 "${rc}"

# ---------------------------------------------------------------------------
# Scenario D — guard: a duplicate annotation is a hard fail.
# ---------------------------------------------------------------------------
D="${WORKDIR}/D"; mkdir -p "${D}"
make_gate "${D}" verify-alpha.sh '# verify-bucket: inv' '# verify-bucket: lint'
make_gate "${D}" verify-bucket-coverage.sh '# verify-bucket: lint'
set +e
out="$(GOCELL_VERIFY_HACK_DIR="${D}" bash "${GUARD}" 2>&1)"; rc=$?
set -e
assert_exit "guard/duplicate-annotation fails" 1 "${rc}"

# ---------------------------------------------------------------------------
# Scenario E — guard anti-vacuity: an empty gate dir is a hard fail (the
# scan must not silently pass on zero gates).
# ---------------------------------------------------------------------------
E="${WORKDIR}/E"; mkdir -p "${E}"
set +e
out="$(GOCELL_VERIFY_HACK_DIR="${E}" bash "${GUARD}" 2>&1)"; rc=$?
set -e
assert_exit "guard/empty-dir fails (anti-vacuity)" 1 "${rc}"

# ---------------------------------------------------------------------------
# Scenario F — driver: VERIFY_BUCKET + VERIFY_DRY_RUN routes only the
# matching bucket's gates and skips the rest.
# ---------------------------------------------------------------------------
F="${WORKDIR}/F"; mkdir -p "${F}"
make_gate "${F}" verify-alpha.sh '# verify-bucket: inv'
make_gate "${F}" verify-beta.sh '# verify-bucket: lint'
make_gate "${F}" verify-gamma.sh '# verify-bucket: inv'
set +e
out="$(GOCELL_VERIFY_HACK_DIR="${F}" VERIFY_BUCKET=inv VERIFY_DRY_RUN=1 bash "${DRIVER}" 2>&1)"; rc=$?
set -e
assert_exit "driver/bucket dry-run exits 0" 0 "${rc}"
assert_contains "driver/bucket includes inv gate" "${out}" "verify-alpha.sh"
assert_not_contains "driver/bucket excludes lint gate" "${out}" "verify-beta.sh"

# ---------------------------------------------------------------------------
# Scenario G — driver anti-vacuity: a bucket that matches no gate (typo) is a
# hard fail rather than a silent green.
# ---------------------------------------------------------------------------
set +e
out="$(GOCELL_VERIFY_HACK_DIR="${F}" VERIFY_BUCKET=typo VERIFY_DRY_RUN=1 bash "${DRIVER}" 2>&1)"; rc=$?
set -e
assert_exit "driver/unknown-bucket fails (anti-vacuity)" 1 "${rc}"

# ---------------------------------------------------------------------------
# Scenario H — driver: an un-annotated gate in bucket mode is a hard fail
# (it would otherwise be silently dropped from every parallel leg).
# ---------------------------------------------------------------------------
H="${WORKDIR}/H"; mkdir -p "${H}"
make_gate "${H}" verify-alpha.sh '# verify-bucket: inv'
make_gate "${H}" verify-orphan.sh        # no annotation
set +e
out="$(GOCELL_VERIFY_HACK_DIR="${H}" VERIFY_BUCKET=inv VERIFY_DRY_RUN=1 bash "${DRIVER}" 2>&1)"; rc=$?
set -e
assert_exit "driver/unannotated-in-bucket-mode fails" 1 "${rc}"

# ---------------------------------------------------------------------------
# Summary + anti-vacuity on the check count itself.
# ---------------------------------------------------------------------------
echo "----------------------------------------"
echo "checks: ${CHECK_COUNT} (pass=${PASS_COUNT} fail=${FAIL_COUNT})"
if [[ "${CHECK_COUNT}" -ne "${EXPECTED_CHECKS}" ]]; then
    echo "ANTI-VACUITY FAIL: ran ${CHECK_COUNT} checks, expected ${EXPECTED_CHECKS} — a scenario was skipped" >&2
    exit 1
fi
if [[ "${FAIL_COUNT}" -gt 0 ]]; then
    echo "bucket-coverage-selftest: ${FAIL_COUNT} FAILED" >&2
    exit 1
fi
echo "bucket-coverage-selftest: all ${PASS_COUNT} checks passed"
