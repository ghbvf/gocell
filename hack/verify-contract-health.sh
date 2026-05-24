#!/usr/bin/env bash
# verify-contract-health runs `gocell check contract-health` to enforce
# contract metadata health rules (CH-*).

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

go run ./cmd/gocell check contract-health
