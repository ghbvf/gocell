#!/usr/bin/env bash
# _preflight.sh — shared dependency preflight for daily-planner lib/ scripts.
# Sourced (not executed) right after `set -euo pipefail`. Single source for the
# "external command must exist before we touch it" check so each lib script no
# longer fails mid-run with an opaque "jq: command not found" under set -e.
#
# Usage (in each lib script):
#   _LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
#   # shellcheck source=_preflight.sh
#   source "$_LIB_DIR/_preflight.sh"
#   require_cmds gh jq python3   # only the subset this script actually uses
#
# require_cmds fails closed: any missing command → loud ERROR (with the install
# hint) on stderr + exit 1, before any real work runs.

# require_cmds <cmd>... — exit 1 if any named command is absent from PATH.
require_cmds() {
  local missing=() c
  for c in "$@"; do
    command -v "$c" >/dev/null 2>&1 || missing+=("$c")
  done
  if [[ ${#missing[@]} -gt 0 ]]; then
    echo "ERROR: missing required command(s): ${missing[*]}" >&2
    local m
    for m in "${missing[@]}"; do
      case "$m" in
        gh)      echo "  install: https://cli.github.com/  (then: gh auth login)" >&2 ;;
        jq)      echo "  install: brew install jq  |  apt-get install jq" >&2 ;;
        python3) echo "  install: brew install python  |  apt-get install python3" >&2 ;;
        *)       echo "  install: $m (see your package manager)" >&2 ;;
      esac
    done
    exit 1
  fi
}
