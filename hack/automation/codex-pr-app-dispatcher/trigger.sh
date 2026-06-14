#!/usr/bin/env bash
# trigger.sh — wake the long-lived Codex PR app dispatcher for an immediate poll.

set -euo pipefail

LABEL="${GOCELL_APP_ROUTER_LAUNCHD_LABEL:-com.ghbvf.gocell.codex-pr-app-dispatcher}"

if ! command -v launchctl >/dev/null 2>&1; then
    echo "trigger: launchctl is required on this host" >&2
    exit 1
fi

launchctl kill SIGUSR1 "gui/$(id -u)/${LABEL}"
