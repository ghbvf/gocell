#!/usr/bin/env bash
# verify-bucket: validate
# verify-local-compose: assert local docker deploy artefacts stay in sync.
#
# Guards the deploy/docker-compose.local.yml / hack/scripts/gen-deploy-secrets.sh /
# docs/ops/local-docker-deploy.md trio + the JWT env-set behavior of
# tests/e2e/start-corebundle.sh that lets real-mode deployments inject a
# fixed keypair instead of every container generating ephemeral keys.
#
# What this checks (executable; fail-closed):
#   1. deploy/docker-compose.local.yml is a structurally valid compose file
#      (`docker compose config -q` parses + interpolates).
#   2. tests/e2e/start-corebundle.sh JWT env-set guard — BEHAVIOR test via
#      fake openssl on PATH:
#        scenario A (env unset): openssl must run and emit PEM
#        scenario B (env set):   openssl must NOT run, keys passed through
#      Replaces an earlier string-grep gate (Soft, violates AI-robust ≥Medium
#      door) with a runtime invariant check on the actual script.
#   3. .gitignore covers .env.local so generated secrets never land in git.
#   4. deploy/.env.local.example + docs/ops/local-docker-deploy.md exist (so the
#      compose file has companion docs / placeholders).
#   5. Makefile exposes local-up / local-down targets.
#
# Companion: hack/verify-shellcheck.sh auto-lints hack/scripts/gen-deploy-secrets.sh
#            AND tests/e2e/start-corebundle.sh (no need to invoke shellcheck here).
#
# Local: bash hack/verify-local-compose.sh
# CI:    invoked transitively via `make verify` (governance.yml).

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
cd "${REPO_ROOT}"

fail() {
  printf 'verify-local-compose: FAIL — %s\n' "$1" >&2
  exit 1
}

# 1. compose structurally valid (interpolation needs env; we supply ephemeral
#    placeholders so config -q does not abort on ${VAR:?err} required vars).
if ! command -v docker >/dev/null 2>&1; then
  printf 'verify-local-compose: skipping compose parse — docker not in PATH\n' >&2
else
  env \
    PG_PASSWORD=placeholder \
    GOCELL_APP_PASSWORD=placeholder \
    RABBITMQ_PASSWORD=placeholder \
    CONFIGCORE_MASTER_KEY=00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff \
    CONFIGCORE_CURSOR_KEY=placeholder-32-bytes-padding-xxxxxxxxxxxxxxxx \
    AUDITCORE_HMAC_KEY=placeholder-32-bytes-padding-xxxxxxxxxxxxxxxxxx \
    AUDITCORE_CURSOR_KEY=placeholder-32-bytes-padding-xxxxxxxxxxxxxxxx \
    ACCESSCORE_CURSOR_KEY=placeholder-32-bytes-padding-xxxxxxxxxxxxxxxx \
    ACCESSCORE_IP_HASH_SALT=placeholder-32-bytes-padding-xxxxxxxxxxxxxxxx \
    SERVICE_SECRET=placeholder-32-bytes-padding-xxxxxxxxxxxxxxxxxxxxxx \
    METRICS_TOKEN=placeholder \
    READYZ_VERBOSE_TOKEN=placeholder \
    OPS_USER=ops \
    OPS_PASS=placeholder \
    GOCELL_JWT_PRIVATE_KEY=placeholder \
    GOCELL_JWT_PUBLIC_KEY=placeholder \
    docker compose -f deploy/docker-compose.local.yml --project-directory . config -q \
    || fail "deploy/docker-compose.local.yml failed structural parse"
fi

# 2. JWT env-set guard — BEHAVIOR test (not string grep).
#    Set up an isolated test sandbox with a fake openssl that touches a marker
#    file on every invocation, and a fake corebundle binary (`/bin/true`). Run
#    the real start-corebundle.sh twice — once with no JWT env (openssl MUST
#    run) and once with both env vars set (openssl MUST NOT run). Either side
#    failing means the guard regressed.
test_dir="$(mktemp -d)"
trap 'rm -rf "${test_dir}"' EXIT

mkdir -p "${test_dir}/bin" "${test_dir}/keys"

cat > "${test_dir}/bin/openssl" <<'EOF'
#!/bin/sh
# Fake openssl for verify-local-compose behavior test. Touches a marker file
# so the test can assert whether the real start-corebundle.sh invoked openssl,
# and writes minimal stub PEM content into the requested -out path so the
# script's subsequent `cat "${keys_dir}/jwt-*.pem"` does not fail.
: > "${TEST_MARKER_DIR}/openssl-called"
out=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "-out" ]; then
    out="${arg}"
  fi
  prev="${arg}"
done
if [ -n "${out}" ]; then
  echo "fake-pem-stub" > "${out}"
fi
exit 0
EOF
chmod +x "${test_dir}/bin/openssl"

# Fake corebundle: `exec`-ed by start-corebundle.sh, must be a real path so the
# test works on both macOS (where /bin/true does not exist; only /usr/bin/true)
# and Linux. Exit 0 — we only care that the script reaches the exec line.
cat > "${test_dir}/bin/fake-corebundle" <<'EOF'
#!/bin/sh
exit 0
EOF
chmod +x "${test_dir}/bin/fake-corebundle"

run_startup() {
  # $1 = "unset" or "set"
  rm -f "${test_dir}/openssl-called"
  case "$1" in
    unset)
      env -i \
        PATH="${test_dir}/bin:/usr/bin:/bin" \
        TEST_MARKER_DIR="${test_dir}" \
        GOCELL_KEYS_DIR="${test_dir}/keys" \
        CORE_BUNDLE_BIN="${test_dir}/bin/fake-corebundle" \
        sh tests/e2e/start-corebundle.sh
      ;;
    set)
      env -i \
        PATH="${test_dir}/bin:/usr/bin:/bin" \
        TEST_MARKER_DIR="${test_dir}" \
        GOCELL_KEYS_DIR="${test_dir}/keys" \
        CORE_BUNDLE_BIN="${test_dir}/bin/fake-corebundle" \
        GOCELL_JWT_PRIVATE_KEY="injected-private" \
        GOCELL_JWT_PUBLIC_KEY="injected-public" \
        sh tests/e2e/start-corebundle.sh
      ;;
  esac
}

run_startup unset
[ -f "${test_dir}/openssl-called" ] \
  || fail "start-corebundle.sh: scenario A (env unset) — openssl NOT invoked; ephemeral keypair generation regressed"

run_startup set
[ -f "${test_dir}/openssl-called" ] \
  && fail "start-corebundle.sh: scenario B (env set) — openssl invoked despite injected keypair; env passthrough regressed"

# 3. .gitignore covers .env.local (generated secrets must never land in git).
if ! grep -qE '^\.env\.local$|^\*\.env\.local$' .gitignore; then
  fail ".gitignore missing .env.local entry"
fi

# 4. Companion files exist.
for required in deploy/.env.local.example docs/ops/local-docker-deploy.md deploy/docker-compose.local.yml hack/scripts/gen-deploy-secrets.sh tests/e2e/start-corebundle.sh; do
  [[ -f "${required}" ]] || fail "missing required file: ${required}"
done

# 5. Makefile targets.
for target in 'local-up:' 'local-down:'; do
  if ! grep -qF "${target}" Makefile; then
    fail "Makefile missing target ${target}"
  fi
done

printf 'verify-local-compose: OK\n'
