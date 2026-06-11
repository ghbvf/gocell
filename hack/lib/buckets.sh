#!/usr/bin/env bash
# buckets.sh — single source for the `# verify-bucket: <name>` annotation that
# routes each hack/verify-*.sh gate into a CI parallel bucket.
#
# The annotation is the SOLE source of truth for the governance lane fan-out:
#   - .github/workflows/governance.yml DERIVES its matrix from it
#     (generate-buckets job → gocell::buckets::list),
#   - hack/make-rules/verify.sh ROUTES gates by it (VERIFY_BUCKET),
#   - hack/verify-bucket-coverage.sh GUARDS that every gate declares exactly one
#     valid value (anti-vacuity).
# Keeping the read in one helper stops the three consumers from drifting on the
# regex. See ADR 202606-1817-adr-governance-lane-parallelization.
#
# Bucket name grammar: ^[a-z][a-z0-9-]*$ (lowercase kebab). This excludes every
# JSON / shell metacharacter (quote, backslash, newline, $, comma), so a derived
# bucket name is safe to embed UNESCAPED — e.g. in the governance.yml
# generate-buckets matrix JSON. Do not widen this grammar without re-checking
# every embedding site.

# gocell::buckets::annotation <gate-file>
# Echo the gate's declared bucket, or nothing when the file does not have
# exactly one well-formed `# verify-bucket: <name>` line. This is the LENIENT
# reader used by the driver and the matrix derivation: a missing, malformed, or
# duplicated annotation yields empty (the driver then fails fast in bucket
# mode). hack/verify-bucket-coverage.sh is the authoritative validator that
# turns each of those shapes into a precise hard failure.
gocell::buckets::annotation() {
    local file="$1" count
    count="$(grep -cE '^# verify-bucket:' "${file}" 2>/dev/null || true)"
    [[ "${count}" == "1" ]] || return 0
    grep -E '^# verify-bucket:[[:space:]]+[a-z][a-z0-9-]*[[:space:]]*$' "${file}" 2>/dev/null \
        | sed -E 's/^# verify-bucket:[[:space:]]+//; s/[[:space:]]+$//'
}

# gocell::buckets::list <hack-dir>
# Echo the unique bucket names declared across every verify-*.sh gate in
# <hack-dir>, one per line, sorted. Gates with no/invalid annotation contribute
# nothing (the coverage guard rejects those separately, so on a green tree the
# list is exhaustive).
gocell::buckets::list() {
    local dir="$1" file bucket
    {
        while IFS= read -r file; do
            bucket="$(gocell::buckets::annotation "${file}")"
            [[ -n "${bucket}" ]] && printf '%s\n' "${bucket}"
        done < <(find "${dir}" -maxdepth 1 -name 'verify-*.sh' -type f | sort)
    } | sort -u
}
