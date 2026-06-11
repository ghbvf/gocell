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
# shellcheck source=../lib/buckets.sh
source "${ROOT}/hack/lib/buckets.sh"

# GOCELL_VERIFY_HACK_DIR overrides the gate-discovery directory (default hack/).
# Only the bucket-coverage selftest sets it, to aim the glob at synthetic
# fixtures; production (`make verify`) always uses hack/.
hack_dir="${GOCELL_VERIFY_HACK_DIR:-hack}"

scripts=()
while IFS= read -r f; do
    scripts+=("${f}")
done < <(find "${hack_dir}" -maxdepth 1 -name 'verify-*.sh' -type f | sort)

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

# VERIFY_BUCKET routes the CI parallel legs: when set, only gates annotated
# `# verify-bucket: <VERIFY_BUCKET>` run. The governance.yml matrix is DERIVED
# from these annotations (generate-buckets job → gocell::buckets::list), so a
# leg always names a real bucket by construction. Local `make verify` leaves
# VERIFY_BUCKET empty and runs the full glob set (unchanged behaviour).
# VERIFY_DRY_RUN lists the resolved gates without executing them (used by the
# bucket-coverage selftest; also handy for debugging a leg's membership).
bucket="${VERIFY_BUCKET:-}"
if [[ -n "${bucket}" ]]; then
    if ! [[ "${bucket}" =~ ^[a-z][a-z0-9-]*$ ]]; then
        gocell::log::error "VERIFY_BUCKET '${bucket}' is not a valid bucket name (^[a-z][a-z0-9-]*\$)"
        exit 1
    fi
    # In bucket mode every discovered gate MUST declare a routable bucket: an
    # un-annotated gate would be silently dropped from every parallel leg and
    # never run in CI. Fail fast before executing anything. (hack/verify-bucket-
    # coverage.sh is the durable, selftest-backed guard for the same property;
    # this is the driver-side defence so a leg can never run a partial set.)
    for script in "${scripts[@]}"; do
        if [[ -z "$(gocell::buckets::annotation "${script}")" ]]; then
            gocell::log::error "$(basename "${script}") has no valid '# verify-bucket:' annotation (required when VERIFY_BUCKET is set)"
            exit 1
        fi
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
    # Bucket filter: gates in other buckets are not this leg's responsibility.
    # They are dropped silently (no results row) so the per-leg job summary
    # lists only the gates this bucket actually owns.
    if [[ -n "${bucket}" && "$(gocell::buckets::annotation "${script}")" != "${bucket}" ]]; then
        continue
    fi
    if [[ -n "${VERIFY_DRY_RUN:-}" ]]; then
        ran=$((ran + 1))
        results+=("${name}|DRYRUN")
        gocell::log::status "WOULD-RUN: ${name}"
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
                PASS)   echo "| \`${gate}\` | ✅ PASS |" ;;
                SKIP)   echo "| \`${gate}\` | ⏭️ SKIP |" ;;
                DRYRUN) echo "| \`${gate}\` | 🔎 DRY-RUN |" ;;
                *)      echo "| \`${gate}\` | ❌ FAIL |" ;;
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
    if [[ -n "${bucket}" ]]; then
        # anti-vacuity for the bucket fan-out: a leg that matches zero gates is
        # a typo'd bucket or a bucket no gate declares — fail rather than pass
        # green and silently never run those checks.
        gocell::log::error "VERIFY_BUCKET='${bucket}' matched no gate — typo, or no gate declares this bucket"
    else
        gocell::log::error "all ${#scripts[@]} verify gates were skipped — VERIFY_SKIP is too broad"
    fi
    exit 1
fi
if [[ ${skipped} -gt 0 ]]; then
    gocell::log::status "All ${ran} verify gates passed (${skipped} of ${#scripts[@]} not run: VERIFY_SKIP / VERIFY_BUCKET filter)."
else
    gocell::log::status "All ${ran} verify gates passed."
fi
