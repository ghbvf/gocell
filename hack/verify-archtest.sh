#!/usr/bin/env bash
# verify-archtest runs the architectural unit-test suite (LAYER-*, AUTH-*,
# SEC-FAIL-CLOSED-*, ERROR-FIRST-API-01, META-QUERYPARAM-DRIFT, ADV-06, etc.).
#
# No -race: archtest is pure read-only static analysis (packages.Load + AST/
# types walk + string compare). t.Parallel() subtests share no mutable state;
# they each consume immutable load results or new t.TempDir() scratch dirs.
# Race detector adds 2.5–3.3x runtime for zero signal on this surface, and
# build-test (tools) shard runs the same suite without -race — keep the two
# entry points behaviourally aligned. Race coverage of concurrency-sensitive
# code paths stays in the kernel/runtime build-test shards.

set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

go test ./tools/archtest/... -count=1
