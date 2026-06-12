#!/usr/bin/env bash
# verify-bucket: nightly
# Thin passthrough — archtest's sole executor is now `gocell verify archtest`
# (cmd/gocell/internal/archtestrunner). This file exists only so the
# verify-bucket meta-system (lib/buckets.sh / verify-bucket-coverage.sh /
# make verify) keeps a per-gate entry for the reserved `nightly` bucket.
# All logic (discovery, shard partition, slowgate, json artifact) lives in
# the CLI. CI sharding is driven directly by archtest-nightly.yml.
# GOWORK=off fail-fast is enforced inside the CLI runner.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."
exec go run ./cmd/gocell verify archtest "$@"
