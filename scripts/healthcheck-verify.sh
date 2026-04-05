#!/usr/bin/env bash
# healthcheck-verify.sh
# Boots all Docker Compose services, waits up to 30 s for every service to
# pass its built-in health check, prints status, then tears down.
# Exit 0 on success, non-zero if any service fails within the timeout.

set -euo pipefail

TIMEOUT=30

echo "Starting Docker Compose services..."
docker compose up -d --wait --timeout "${TIMEOUT}"

echo ""
echo "All services healthy."
echo ""
docker compose ps

echo ""
docker compose down
echo "Healthcheck verification complete."
