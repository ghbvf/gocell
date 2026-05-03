#!/usr/bin/env bash
# verify-gitignore-respect: fail if any path in the explicit "must-not-be-tracked"
# allowlist below is currently committed in HEAD.
#
# .gitignore alone is advisory — `git add -f` bypasses it, and once a file is
# tracked, .gitignore stops applying. This gate makes the rule binding for
# build-time generated files that are intentionally per-platform / per-build:
# committing them re-introduces cross-platform drift (see ADR
# 202605040030-adr-wire-format-out-of-kernel.md).
#
# Adding a new entry: append to MUST_NOT_BE_TRACKED and document its rationale
# (typically "build-tag-gated codegen output; committing causes drift").
#
# ref: kubernetes/kubernetes hack/verify-no-vendor-cycles.sh — same exit-code convention.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT}"

# Files that must NEVER be committed. Each is .gitignore'd; this gate is the
# enforcement layer that closes the `git add -f` loophole.
MUST_NOT_BE_TRACKED=(
    # cmd/corebundle/catalog_gen.go — built-tag gated codegen output. The real
    # graph is per-platform (go/packages.Load quirk on _test.go imports).
    # Committing re-introduces cross-platform drift; the stub at
    # cmd/corebundle/catalog_gen_stub.go covers the default build path.
    "cmd/corebundle/catalog_gen.go"
)

failed=0
for path in "${MUST_NOT_BE_TRACKED[@]}"; do
    if git ls-files --error-unmatch "${path}" >/dev/null 2>&1; then
        echo "FAIL: ${path} is tracked by git but must not be."
        echo "      Run 'git rm --cached ${path}' to untrack (file is .gitignore'd)."
        echo "      Rationale: this file is build-tag-gated codegen; committing"
        echo "      it re-introduces cross-platform drift."
        failed=1
    fi
done

if [[ "${failed}" -eq 1 ]]; then
    exit 1
fi

echo "PASS: no .gitignore'd build-tag-gated files are tracked"
