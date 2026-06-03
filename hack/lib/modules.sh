#!/usr/bin/env bash
# Workspace module enumeration helpers for hack/verify-*.sh scripts.
# Source this file; do NOT execute it directly.

# gocell::modules::dirs emits each workspace module's DiskPath (relative to go.work),
# one per line, sourced from go.work via the official `go work edit -json` API.
# Single source of truth = go.work; do NOT hardcode a module array (would drift).
# Future modules appear automatically when added to go.work `use`.
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

    printf '%s\n' "${json}" | jq -r '.Use[].DiskPath'
}
