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
# Sync requirements (manual, not statically enforced):
#
#   1. .github/workflows/_build-lint.yml's golangci-lint-action `version:`
#      MUST equal GOLANGCI_LINT_VERSION below. The lint-action is kept for
#      its build cache and `config verify`; the version literal is the
#      source of truth replicated there.
#
#   2. The repo's go.mod `mvdan.cc/gofumpt` version MUST equal the version
#      vendored by the pinned golangci-lint release (v2.11.4 vendors
#      mvdan.cc/gofumpt v0.9.2). Producer-side round-trip tests in
#      tools/codegen call gofumpt.Source via the project lib, while the CI
#      formatter gate calls the same package vendored inside golangci-lint;
#      a version split here means producer output passes producer tests
#      but trips the gate (or vice versa) — exactly the drift this rollout
#      closes on the consumer side.
#
# Reviewer-enforced sync — no archtest because both upstreams are yaml /
# go.mod literals shellcheck/Go can't cross-reference cleanly.
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
    # `go install` writes the binary to GOBIN if set, otherwise to the first
    # entry of GOPATH's bin/ subdirectory. Probing only ${GOPATH}/bin/ misses
    # the GOBIN case and the multi-path GOPATH case (colon-separated list);
    # both are common on developer laptops with custom toolchains.
    local install_dir
    install_dir="$(go env GOBIN)"
    if [[ -z "${install_dir}" ]]; then
        local gopath
        gopath="$(go env GOPATH)"
        install_dir="${gopath%%:*}/bin"
    fi
    local binary="${install_dir}/golangci-lint"
    local version_no_v="${GOLANGCI_LINT_VERSION#v}"

    if [[ -x "${binary}" ]]; then
        # `--version` output varies between v1 ("has version v1.x.y") and v2
        # ("has version 2.x.y built ..."). Parse the first numeric token of
        # the form X.Y.Z and compare equal — substring-grep would accept
        # bogus matches like 2.11.4 inside 2.11.40 or 12.11.4.
        local got
        got="$("${binary}" --version 2>/dev/null | head -n1 | awk '{
            for (i = 1; i <= NF; i++) {
                if ($i ~ /^v?[0-9]+\.[0-9]+\.[0-9]+$/) {
                    sub(/^v/, "", $i)
                    print $i
                    exit
                }
            }
        }')"
        if [[ "${got}" == "${version_no_v}" ]]; then
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
