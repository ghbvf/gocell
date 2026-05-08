#!/usr/bin/env bash
set -euo pipefail

root="${1:-.}"
cd "$root"

tagged_dirs_file="$(mktemp)"
trap 'rm -f "$tagged_dirs_file"' EXIT

while IFS= read -r -d '' file; do
  line="$(grep -m1 '^//go:build[[:space:]]' "$file" || true)"
  [[ -n "$line" ]] || continue
  expr="${line#//go:build }"
  expr="${expr//(/ }"
  expr="${expr//)/ }"
  expr="${expr//!/ }"
  expr="${expr//&&/ }"
  expr="${expr//||/ }"
  expr="${expr//,/ }"
  for tok in $expr; do
    if [[ "$tok" == "integration" || "$tok" == "e2e" ]]; then
      dir="$(cd "$(dirname "$file")" && pwd -P)"
      printf '%s\n' "$dir" >> "$tagged_dirs_file"
      break
    fi
  done
done < <(find . -name '*_test.go' -type f -print0)
sort -u -o "$tagged_dirs_file" "$tagged_dirs_file"

while IFS=$'\t' read -r dir import_path; do
  if ! grep -Fxq "$dir" "$tagged_dirs_file"; then
    continue
  fi
  if [[ "$import_path" == */tests/e2e ]]; then
    continue
  fi
  printf '%s\n' "$import_path"
done < <(go list -f '{{.Dir}}{{"\t"}}{{.ImportPath}}' ./...)
