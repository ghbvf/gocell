#!/usr/bin/env bash
# verify-govalidate runs `gocell validate --strict` to enforce metadata
# governance rules (FMT, ADV, REF, LAYER, VERIFY, CH, CONTRACT-CONSISTENCY).

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

go run ./cmd/gocell validate --strict
