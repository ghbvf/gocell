#!/usr/bin/env bash
# Single-source golangci-lint version + bootstrap helper.
#
# Anything in the repo that needs golangci-lint MUST source this file and
# call gocell::golangci_lint::ensure to get a binary path pinned to
# GOLANGCI_LINT_VERSION. Direct invocation of `golangci-lint` from PATH is
# forbidden because the host's ambient version may drift from the CI lint
# shard's pinned version, producing different formatter / linter verdicts
# locally vs CI — exactly the failure mode the gofumpt rollout was meant
# to prevent on the producer side.
#
# Sync requirement (manual, not statically enforced):
#   .github/workflows/_build-lint.yml's golangci-lint-action `version:`
#   MUST equal GOLANGCI_LINT_VERSION below. The lint-action is kept for
#   its build cache and `config verify`; the version literal is the
#   source of truth replicated there. Reviewer-enforced — there is no
#   archtest because the upstream is yaml.
#
# ref: kubernetes/kubernetes hack/verify-golangci-lint.sh — same
# go-install-from-pinned-version pattern; we omit the docker fallback
# K8s uses for multi-thousand-contributor reproducibility.
# ref: prometheus/prometheus Makefile.common GOLANGCI_LINT_VERSION —
# same single-version-constant convention shared by fmt and verify.

GOLANGCI_LINT_VERSION="v2.11.4"

# gocell::golangci_lint::ensure prints the absolute path to a golangci-lint
# binary at GOLANGCI_LINT_VERSION on stdout. Bootstrap progress / errors
# go to stderr so callers can `binary="$(gocell::golangci_lint::ensure)"`
# without contaminating the path with status text.
gocell::golangci_lint::ensure() {
    local gopath
    gopath="$(go env GOPATH)"
    local binary="${gopath}/bin/golangci-lint"
    local version_no_v="${GOLANGCI_LINT_VERSION#v}"

    if [[ -x "${binary}" ]]; then
        # `--version` output varies between v1 ("has version v1.x.y") and v2
        # ("has version 2.x.y built ..."). Substring-match the numeric core
        # so both forms accept; this also tolerates the build-info suffix.
        if "${binary}" --version 2>/dev/null | head -n1 | grep -q "${version_no_v}"; then
            echo "${binary}"
            return 0
        fi
    fi

    echo "bootstrapping golangci-lint ${GOLANGCI_LINT_VERSION} (one-time, ~30s on cold module cache)..." >&2
    # GOFLAGS unset so user's -mod=vendor / build tags don't affect a tool
    # install. The tool lives outside the project module graph.
    if ! GOFLAGS="" go install "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@${GOLANGCI_LINT_VERSION}" >&2; then
        echo "gocell::golangci_lint::ensure: go install failed" >&2
        return 1
    fi
    echo "${binary}"
}
