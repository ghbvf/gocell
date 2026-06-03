#!/usr/bin/env bash
# verify-workspace asserts the go.work workspace is consistent and every member
# module builds. The module-enumeration funnel (hack/lib/modules.sh, sourced from
# go.work) is the extension point for future cross-module go build/test traversal.

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

# shellcheck source=lib/util.sh
source hack/lib/util.sh
# shellcheck source=lib/modules.sh
source hack/lib/modules.sh

# (a) Assert go.work exists and parses as a valid workspace.
gocell::log::status "Checking go.work exists and is valid"
if [[ ! -f go.work ]]; then
    gocell::log::error "go.work not found in repo root"
    exit 1
fi
if ! go work edit -json >/dev/null; then
    gocell::log::error "go work edit -json failed — go.work is malformed"
    exit 1
fi

# (b) go.work sync drift check: run go work sync, then assert no diff on go.work.
gocell::log::status "Running go work sync (drift check)"
go work sync
if ! git diff --exit-code -- go.work; then
    gocell::log::error "go.work has drift after 'go work sync' — commit the updated go.work"
    exit 1
fi

# (c) Enumerate modules; assert list is non-empty and contains root '.'.
gocell::log::status "Enumerating workspace modules"
module_dirs=()
while IFS= read -r dir; do
    module_dirs+=("${dir}")
done < <(gocell::modules::dirs)

if [[ ${#module_dirs[@]} -eq 0 ]]; then
    gocell::log::error "gocell::modules::dirs returned an empty list — go.work has no 'use' entries"
    exit 1
fi

found_root=0
for dir in "${module_dirs[@]}"; do
    if [[ "${dir}" == "." ]]; then
        found_root=1
        break
    fi
done
if [[ "${found_root}" -eq 0 ]]; then
    gocell::log::error "workspace module list does not contain '.' (root module) — anti-vacuity check failed"
    exit 1
fi

gocell::log::status "Workspace modules: ${module_dirs[*]}"

# (d) Build each module.
for dir in "${module_dirs[@]}"; do
    gocell::log::status "Building module: ${dir}"
    if ! go -C "${dir}" build ./...; then
        gocell::log::error "go build ./... failed in module '${dir}'"
        exit 1
    fi
done

gocell::log::status "verify-workspace: all checks passed"
