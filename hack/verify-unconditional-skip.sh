#!/usr/bin/env bash
# verify-unconditional-skip rejects test files whose t.Skip is unconditional —
# any blanket skip is a hidden disabled test and must be either deleted or
# guarded with a runtime predicate.

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

go run ./cmd/gocell check unconditional-skip ./...
