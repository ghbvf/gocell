#!/usr/bin/env bash
# monitor.sh — manage the local Codex PR app dispatcher LaunchAgent.

set -euo pipefail

LABEL="${GOCELL_APP_ROUTER_LAUNCHD_LABEL:-com.ghbvf.gocell.codex-pr-app-dispatcher}"
PLIST="${GOCELL_APP_ROUTER_LAUNCHD_PLIST:-${HOME}/Library/LaunchAgents/${LABEL}.plist}"
ROUTER_HOME="${GOCELL_APP_ROUTER_HOME:-${HOME}/.local/gocell-pr-app-router}"
DOMAIN="gui/$(id -u)"
SERVICE="${DOMAIN}/${LABEL}"

usage() {
    cat >&2 <<EOF
usage: $0 <start|stop|restart|status|trigger|logs>

Environment overrides:
  GOCELL_APP_ROUTER_LAUNCHD_LABEL   default: ${LABEL}
  GOCELL_APP_ROUTER_LAUNCHD_PLIST   default: ${PLIST}
  GOCELL_APP_ROUTER_HOME            default: ${ROUTER_HOME}
EOF
}

require_launchctl() {
    if ! command -v launchctl >/dev/null 2>&1; then
        echo "monitor: launchctl is required on this host" >&2
        exit 1
    fi
}

require_plist() {
    if [[ ! -f "${PLIST}" ]]; then
        echo "monitor: LaunchAgent plist not found: ${PLIST}" >&2
        exit 1
    fi
}

start() {
    require_launchctl
    require_plist
    launchctl bootstrap "${DOMAIN}" "${PLIST}" 2>/dev/null || true
    launchctl kickstart -k "${SERVICE}"
}

stop() {
    require_launchctl
    require_plist
    launchctl bootout "${DOMAIN}" "${PLIST}" 2>/dev/null || true
}

status() {
    require_launchctl
    launchctl print "${SERVICE}"
}

trigger() {
    require_launchctl
    launchctl kill SIGUSR1 "${SERVICE}"
}

logs() {
    tail -n "${GOCELL_APP_ROUTER_LOG_LINES:-80}" \
        "${ROUTER_HOME}/logs/router.log" \
        "${ROUTER_HOME}/logs/router.err.log"
}

cmd="${1:-}"
case "${cmd}" in
    start) start ;;
    stop) stop ;;
    restart) stop; start ;;
    status) status ;;
    trigger) trigger ;;
    logs) logs ;;
    *) usage; exit 64 ;;
esac
