#!/usr/bin/env bash
# trigger.sh — wake the long-lived Codex PR app dispatcher for an immediate poll.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"

exec "${SCRIPT_DIR}/monitor.sh" trigger
