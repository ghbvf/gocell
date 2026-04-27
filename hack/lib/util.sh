#!/usr/bin/env bash
# Shared logging helpers for hack/verify-*.sh scripts.

gocell::log::status() {
    printf '+++ [%s] %s\n' "$(date +%H:%M:%S)" "$*"
}

gocell::log::error() {
    printf '!!! [%s] %s\n' "$(date +%H:%M:%S)" "$*" >&2
}
