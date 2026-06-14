#!/usr/bin/env bash
# rename-framework-module.sh — rewrite GoCell framework import paths after the
# kernel/runtime/pkg → framework/ module split (#1565).
#
# WHAT IT DOES (and ONLY this): rewrites the three framework-layer import
# prefixes in every tracked *.go file —
#
#     github.com/ghbvf/gocell/kernel/…   → github.com/ghbvf/gocell/framework/kernel/…
#     github.com/ghbvf/gocell/runtime/…  → github.com/ghbvf/gocell/framework/runtime/…
#     github.com/ghbvf/gocell/pkg/…      → github.com/ghbvf/gocell/framework/pkg/…
#
# WHY ONLY IMPORTS: this is the re-runnable part a STALE in-flight branch needs.
# After rebasing onto post-#1565 develop, the branch already inherits the new
# go.work / go.mod / .golangci.yml from develop; only the branch's OWN new .go
# files still carry stale framework imports. Run this from the repo root on such
# a branch to fix them.
#
# WHAT IT DELIBERATELY DOES NOT TOUCH:
#   - bare-root "github.com/ghbvf/gocell" literals — the SAME string means
#     different things in different sites (org/repo prefix in layer.go's
#     gocellPrefix and archtest PlatformModulePath; framework module root in
#     codegen modulePrefix). Each is reasoned by hand in the #1565 PR, never
#     blanket-rewritten. The three prefixes above are the only unambiguous,
#     mechanically-safe rewrites.
#   - go.mod / go.work / .golangci.yml / CI configs — one-time PR edits that a
#     rebasing branch gets from develop, not codemod targets.
#   - adapters/ corecells/ cellmodules/ cmd/ examples/ tests/ tools/ generated/
#     import paths — these modules do NOT move; their prefixes lack the
#     /kernel|/runtime|/pkg segment right after the module root, so the anchored
#     regex below cannot match them.
#
# IDEMPOTENT: on an already-migrated tree every match target is now preceded by
# /framework/, so the regex (which requires gocell/ immediately followed by
# kernel|runtime|pkg) matches nothing → zero changes. Re-running is a safe no-op
# (asserted by the #1565 acceptance gate: `git diff` must be empty afterward).
#
# ref: golang.org/x/tools/gopls module layout (directory segment in import path).
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

# Anchored rewrite: match "github.com/ghbvf/gocell/" immediately followed by one
# of the three layer dirs and a delimiter (path "/" or closing import quote "),
# inserting "framework/". Capturing the delimiter ($2) re-emits it verbatim so a
# bare ".../kernel" (no subpackage) is handled identically to ".../kernel/foo".
# $1/$2 are perl backrefs, NOT shell vars — single quotes are required so the
# shell passes the pattern through verbatim (SC2016 is the expected, correct case).
# shellcheck disable=SC2016
readonly PERL_REWRITE='s{github\.com/ghbvf/gocell/(kernel|runtime|pkg)(["/])}{github.com/ghbvf/gocell/framework/$1$2}g'

# Portable (bash 3.2, no mapfile) + byte-exact: `perl -i.bak -pe` edits in place
# line-by-line, preserving the file's trailing newline exactly; `cmp -s` against
# the backup detects whether anything actually changed (for the count + the
# idempotency no-op guarantee). NUL-delimited read survives any path. Changed
# files are collected for the re-sort pass below.
changed_files=()
while IFS= read -r -d '' f; do
    perl -i.bak -pe "${PERL_REWRITE}" -- "${f}"
    if ! cmp -s "${f}" "${f}.bak"; then
        changed_files+=("${f}")
    fi
    rm -f "${f}.bak"
done < <(git ls-files -z '*.go')

echo "rename-framework-module: rewrote framework imports in ${#changed_files[@]} file(s)"

# Re-sort imports in the rewritten files. The path edit changes alphabetical
# order WITHIN the existing in-repo import group (e.g. ".../framework/kernel"
# now sorts before ".../generated"); the group set itself is unchanged because
# .golangci.yml local-prefixes is the org prefix "github.com/ghbvf/gocell"
# (covering every in-repo module, framework included), so `gofmt -w` — stdlib,
# always present, a strict subset of the gate's gofumpt — produces the byte-exact
# import ordering the gofumpt gate expects. Without this the rewrite leaves
# mis-sorted imports that fail `golangci-lint fmt`.
if [[ ${#changed_files[@]} -gt 0 ]]; then
    gofmt -w -- "${changed_files[@]}"
    echo "rename-framework-module: re-sorted imports in ${#changed_files[@]} file(s)"
fi
