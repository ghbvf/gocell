#!/bin/sh
# start-corebundle.sh — corebundle container entrypoint.
#
# Generates an ephemeral RSA 2048 JWT keypair into ${GOCELL_KEYS_DIR}/ when no
# keypair is provided via env (default for tests/e2e and local-dev single-pod);
# passes through env-injected keys untouched (production K8s Secret path).
#
# Env hooks (defaults match the Dockerfile image layout — never override in
# production):
#   GOCELL_KEYS_DIR  — directory for ephemeral PEM files (default /run/gocell;
#                      tmpfs-backed in compose so keys disappear on stop)
#   CORE_BUNDLE_BIN  — corebundle binary path (default /usr/local/bin/corebundle)
#
# Both overrides exist solely so hack/verify-local-compose.sh can exercise the
# two env branches (set / unset) with a fake openssl on PATH and a noop binary
# in place of corebundle, without requiring a real container.

set -eu

keys_dir="${GOCELL_KEYS_DIR:-/run/gocell}"
core_bundle_bin="${CORE_BUNDLE_BIN:-/usr/local/bin/corebundle}"

if [ -z "${GOCELL_JWT_PRIVATE_KEY:-}" ]; then
  openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 \
    -out "${keys_dir}/jwt-private.pem" 2>/dev/null
  openssl rsa -in "${keys_dir}/jwt-private.pem" -pubout \
    -out "${keys_dir}/jwt-public.pem" 2>/dev/null
  chmod 600 "${keys_dir}/jwt-private.pem"
  GOCELL_JWT_PRIVATE_KEY="$(cat "${keys_dir}/jwt-private.pem")"
  GOCELL_JWT_PUBLIC_KEY="$(cat "${keys_dir}/jwt-public.pem")"
  export GOCELL_JWT_PRIVATE_KEY GOCELL_JWT_PUBLIC_KEY
fi

exec "${core_bundle_bin}" "$@"
