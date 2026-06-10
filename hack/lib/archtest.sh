#!/usr/bin/env bash
# Single source of the archtest leaf build tag — ARCHTEST-LEAF-BUILD-TAG-01.
# Owners source this file and pass -tags="$ARCHTEST_BUILD_TAGS" to go test.
# Changing the tag name here propagates to all three owner scripts at once.

export ARCHTEST_BUILD_TAGS="archtest"
readonly ARCHTEST_BUILD_TAGS
