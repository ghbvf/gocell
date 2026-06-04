#!/usr/bin/env bash
# Workspace module enumeration helpers for hack/verify-*.sh scripts.
# Source this file; do NOT execute it directly.

# gocell::modules::dirs emits each workspace module's DiskPath (relative to the
# go.work directory), one per line, sourced from go.work via the official
# `go work edit -json` API. Single source of truth = go.work; do NOT hardcode a
# module array (would drift). Future modules appear automatically when added to
# go.work `use`.
#
# Fail-closed path validation: every DiskPath is checked (non-empty, relative,
# no `..` segment, resolves inside the repo root, contains a go.mod) before being
# emitted, so a malformed `go.work use` entry (e.g. `use ../outside`) aborts with
# a clear error rather than handing an out-of-repo path to `go -C`. Mirrors the
# escape protection kernel/metadata/locator_manifest.go applies to manifest
# module paths.
#
# Callers MUST consume this via command substitution
# (`out="$(gocell::modules::dirs)" || handle-failure`) so a validation failure
# propagates — a `while read < <(gocell::modules::dirs)` process substitution
# runs the helper in a subshell and would swallow the non-zero return.
gocell::modules::dirs() {
    if ! command -v jq >/dev/null 2>&1; then
        gocell::log::error "jq not found in PATH; install jq to run workspace module enumeration"
        return 1
    fi

    local json
    if ! json="$(go work edit -json 2>&1)"; then
        gocell::log::error "go work edit -json failed: ${json}"
        return 1
    fi

    local repo_root
    repo_root="$(pwd -P)"

    local dir resolved
    while IFS= read -r dir; do
        if [[ -z "${dir}" ]]; then
            gocell::log::error "go.work 'use' entry has an empty DiskPath"
            return 1
        fi
        if [[ "${dir}" == /* ]]; then
            gocell::log::error "go.work 'use' DiskPath is absolute (must be repo-relative): ${dir}"
            return 1
        fi
        if [[ "${dir}" == ".." || "${dir}" == ../* || "${dir}" == */../* || "${dir}" == */.. ]]; then
            gocell::log::error "go.work 'use' DiskPath contains a '..' segment (escapes repo): ${dir}"
            return 1
        fi
        if ! resolved="$(cd "${dir}" 2>/dev/null && pwd -P)"; then
            gocell::log::error "go.work 'use' DiskPath does not resolve to a directory: ${dir}"
            return 1
        fi
        if [[ "${resolved}/" != "${repo_root}/"* ]]; then
            gocell::log::error "go.work 'use' DiskPath resolves outside the repo root: ${dir} -> ${resolved}"
            return 1
        fi
        if [[ ! -f "${dir}/go.mod" ]]; then
            gocell::log::error "go.work 'use' DiskPath has no go.mod: ${dir}"
            return 1
        fi
        printf '%s\n' "${dir}"
    done < <(printf '%s\n' "${json}" | jq -r '.Use[].DiskPath')
}
