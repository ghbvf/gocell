#!/usr/bin/env bash
# verify-shellcheck: lint every project shell script with shellcheck.
#
# Replaces the regex-only verify-shell-safety.sh added in PR #350. The grep
# layer could only assert "the literal token `set -euo pipefail` appears in
# the first 30 lines"; it could not see that `set +o pipefail` re-disables it,
# that errexit is suppressed inside conditional positions (SC2310), or that
# command substitutions silently drop `-e` (SC2311). shellcheck audits those
# execution-time properties.
#
# What this gate does NOT do (intentional, matches K8s/Homebrew practice):
#   * Does not enforce a particular `set -euo pipefail` declaration form.
#     Long form, short form, and equivalent splits all pass.
#   * Does not enable optional checks SC2310 / SC2311 / SC2312 (errexit-
#     suppression hints). They sit at info severity by default; both K8s
#     (default severity floor) and Homebrew (explicit `disable=`) leave
#     them off because the false-positive rate is high in practice.
#   * Does not flag `set +o pipefail`. shellcheck has no rule for it; the
#     industry convention is that intentionally disabling a flag is a code-
#     review concern, not a static-gate concern.
#
# ref: kubernetes/kubernetes hack/verify-shellcheck.sh — shape and exclude
#      list (SC1090,SC1091,SC2230) follow K8s. We diverge from K8s on:
#      version pinning + docker-image fallback (deemed multi-thousand-
#      contributor reproducibility scaffolding, unjustified at our scale)
#      and broad `find . -name "*.sh"` discovery (this repo keeps all shell
#      under scripts/ hack/ tests/).

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "${REPO_ROOT}"

if ! command -v shellcheck >/dev/null 2>&1; then
  cat >&2 <<'EOF'
verify-shellcheck: shellcheck not in PATH.
Install with:
  * macOS:   brew install shellcheck
  * Linux:   apt-get install shellcheck   (Debian / Ubuntu)
             dnf install ShellCheck       (Fedora / RHEL)
EOF
  exit 1
fi

scripts_to_check=()
while IFS= read -r script; do
  ec=0
  git check-ignore -q "${script}" || ec=$?
  case "${ec}" in
    0) ;;                                  # ignored, skip
    1) scripts_to_check+=("${script}") ;;  # not ignored, include
    *) echo "verify-shellcheck: git check-ignore failed for ${script} (exit ${ec})" >&2
       exit "${ec}" ;;                     # 128 = not a repo / bad pathspec
  esac
done < <(find scripts hack tests -type f -name '*.sh' | sort)

if [[ ${#scripts_to_check[@]} -eq 0 ]]; then
  echo "verify-shellcheck: no scripts found under scripts/ hack/ tests/" >&2
  exit 1
fi

exec shellcheck \
  --external-sources \
  --exclude=SC1090,SC1091,SC2230 \
  --color=auto \
  "${scripts_to_check[@]}"
