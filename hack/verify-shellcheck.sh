#!/usr/bin/env bash
# verify-shellcheck: lint every project-owned shell script with shellcheck.
#
# Replaces the regex-only verify-shell-safety.sh added in PR #350. The grep
# layer could only assert "the literal token `set -euo pipefail` appears in
# the first 30 lines"; it could not see that `set +o pipefail` re-disables it,
# that errexit is suppressed inside conditional positions (SC2310), or that
# command substitutions silently drop `-e` (SC2311). shellcheck audits those
# execution-time properties — it is the right tool for the job.
#
# This script mirrors kubernetes/kubernetes hack/verify-shellcheck.sh:
#   * pinned SHELLCHECK_VERSION + matching docker image (sha256-digest),
#   * prefer the host binary if its version matches exactly, otherwise fall
#     back to the official shellcheck container,
#   * `--external-sources` so `source hack/lib/util.sh` resolves,
#   * shared K8s exclude list (SC1090,SC1091,SC2230) — see comments below,
#   * git-aware file discovery (skip .gitignored paths).
#
# What this gate does NOT do (intentional, matches K8s/Homebrew practice):
#   * Does not enforce a particular `set -euo pipefail` declaration form.
#     Long form (`set -o errexit / -o nounset / -o pipefail`), short form
#     (`set -euo pipefail`), or any equivalent split is accepted — exactly
#     as in the K8s tree, where the long form is dominant.
#   * Does not enable optional checks SC2310 / SC2311 / SC2312 (errexit-
#     suppression hints). They sit at info severity by default; both K8s
#     (default severity floor) and Homebrew (explicit `disable=`) leave
#     them off because the false-positive rate on real projects is high.
#   * Does not flag `set +o pipefail`. shellcheck has no rule for it; the
#     industry convention is that intentionally disabling a flag is a code-
#     review concern, not a static-gate concern.
#
# ref: kubernetes/kubernetes hack/verify-shellcheck.sh
#      https://raw.githubusercontent.com/kubernetes/kubernetes/master/hack/verify-shellcheck.sh

set -o errexit
set -o nounset
set -o pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")"/.. && pwd -P)"

DOCKER="${DOCKER:-docker}"

# Required version. Keep SHELLCHECK_VERSION and SHELLCHECK_IMAGE in sync — the
# host binary is only used when its `--version` matches exactly, otherwise we
# fall back to this image so behaviour is deterministic across machines.
SHELLCHECK_VERSION="0.9.0"
SHELLCHECK_IMAGE="docker.io/koalaman/shellcheck:v0.9.0@sha256:f35e8987b02760d4e76fc99a68ad5c42cc10bb32f3dd2143a3cf92f1e5446a45"

# Disabled lints — copied verbatim from K8s verify-shellcheck.sh. Diverging
# from that list should be a deliberate, documented decision.
disabled=(
  # SC1090: non-constant source. We use this pattern (e.g. dynamic helper
  # paths) without known bugs.
  1090
  # SC1091: shellcheck cannot find a sourced file. With nested directory
  # structures and conditional sourcing, this fires too often to be useful.
  1091
  # SC2230: prefer `command -v` over `which`. They are not strictly
  # equivalent and we have no project-wide migration planned.
  2230
)
join_by() {
  local IFS="$1"
  shift
  echo "$*"
}
SHELLCHECK_DISABLED="$(join_by , "${disabled[@]}")"
readonly SHELLCHECK_DISABLED

cd "${REPO_ROOT}"

# Discover scripts. Filtering through `git check-ignore` skips anything
# already excluded from the working tree (vendored copies, generated output,
# spec-kit artefacts under .specify/, etc.) — same pattern K8s uses.
scripts_to_check=()
while IFS=$'\n' read -r script; do
  git check-ignore -q "${script}" || scripts_to_check+=("${script}")
done < <(find . -name "*.sh" \
  -not \( \
    -path ./.git\* -o \
    -path ./bin\* -o \
    -path ./worktrees\* -o \
    -path ./vendor\* -o \
    -path ./.specify\* \
  \))

if [[ ${#scripts_to_check[@]} -eq 0 ]]; then
  echo "verify-shellcheck: no scripts found (unexpected; check find filters)" >&2
  exit 1
fi

# Detect host binary at exact pinned version.
HAVE_SHELLCHECK=false
if command -v shellcheck >/dev/null 2>&1; then
  detected_version="$(shellcheck --version | grep '^version: ' || true)"
  if [[ "${detected_version}" == "version: ${SHELLCHECK_VERSION}" ]]; then
    HAVE_SHELLCHECK=true
  fi
fi

SHELLCHECK_OPTIONS=(
  # Follow `source` directives even when shellcheck wasn't invoked on the
  # sourced file directly (we lint one file at a time so failures pinpoint).
  "--external-sources"
  "--exclude=${SHELLCHECK_DISABLED}"
  "--color=auto"
)

res=0
if "${HAVE_SHELLCHECK}"; then
  echo "Using host shellcheck ${SHELLCHECK_VERSION} binary."
  shellcheck "${SHELLCHECK_OPTIONS[@]}" "${scripts_to_check[@]}" >&2 || res=$?
else
  echo "Host shellcheck ${SHELLCHECK_VERSION} not detected; using docker image ${SHELLCHECK_IMAGE}."
  if ! command -v "${DOCKER}" >/dev/null 2>&1; then
    cat >&2 <<EOF
verify-shellcheck: shellcheck v${SHELLCHECK_VERSION} not in PATH and docker not available.

Install one of:
  * macOS:   brew install shellcheck   (current brew bottle is a newer minor;
                                        version-skew is fine for local dev,
                                        the gate only requires v${SHELLCHECK_VERSION}
                                        for reproducible CI behaviour)
  * Linux:   apt-get install shellcheck     (Ubuntu / Debian)
             dnf install ShellCheck         (Fedora / RHEL)
  * Docker:  ensure 'docker' is on PATH, the gate will pull
             ${SHELLCHECK_IMAGE} on first run.
EOF
    exit 1
  fi
  "${DOCKER}" run \
    --rm -v "${REPO_ROOT}:${REPO_ROOT}" -w "${REPO_ROOT}" --security-opt label=disable \
    "${SHELLCHECK_IMAGE}" \
    "${SHELLCHECK_OPTIONS[@]}" "${scripts_to_check[@]}" >&2 || res=$?
fi

if [[ ${res} -ne 0 ]]; then
  echo "" >&2
  echo "verify-shellcheck: shellcheck reported issues (exit ${res})." >&2
  echo "Fix the findings above. To suppress an unavoidable false positive," >&2
  echo "add a line-scoped 'shellcheck disable=SCxxxx' directive with a" >&2
  echo "comment justifying the exception." >&2
  exit "${res}"
fi

printf 'OK (%d scripts checked)\n' "${#scripts_to_check[@]}"
