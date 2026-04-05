#!/usr/bin/env bash
# kg-verify.sh — Knowledge-Graph verification script
#
# Checks:
#   1. Layer isolation: kernel/ must not import runtime/, adapters/, cells/
#   2. Layer isolation: runtime/ must not import cells/, adapters/
#   3. Layer isolation: cells/ must not cross-import other cells' internal/
#   4. go.mod whitelist: only allowed direct dependencies
#   5. Banned field names: legacy field names must not appear in YAML metadata
#
# Exit 0 on all-pass, non-zero on first failure.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SRC_DIR="${REPO_ROOT}/src"
FAIL=0

red()   { printf '\033[0;31m%s\033[0m\n' "$1"; }
green() { printf '\033[0;32m%s\033[0m\n' "$1"; }
header(){ printf '\n=== %s ===\n' "$1"; }

# ---------------------------------------------------------------
# 1. kernel/ must not import runtime/, adapters/, cells/
# ---------------------------------------------------------------
header "Check 1: kernel/ layer isolation"

KERNEL_VIOLATIONS=$(grep -rn '"github.com/ghbvf/gocell/\(runtime\|adapters\|cells\)' "${SRC_DIR}/kernel/" --include='*.go' 2>/dev/null || true)
if [ -n "${KERNEL_VIOLATIONS}" ]; then
    red "FAIL: kernel/ imports forbidden layers:"
    echo "${KERNEL_VIOLATIONS}"
    FAIL=1
else
    green "PASS: kernel/ has no forbidden imports"
fi

# ---------------------------------------------------------------
# 2. runtime/ must not import cells/ or adapters/
# ---------------------------------------------------------------
header "Check 2: runtime/ layer isolation"

RUNTIME_VIOLATIONS=$(grep -rn '"github.com/ghbvf/gocell/\(cells\|adapters\)' "${SRC_DIR}/runtime/" --include='*.go' 2>/dev/null || true)
if [ -n "${RUNTIME_VIOLATIONS}" ]; then
    red "FAIL: runtime/ imports forbidden layers:"
    echo "${RUNTIME_VIOLATIONS}"
    FAIL=1
else
    green "PASS: runtime/ has no forbidden imports"
fi

# ---------------------------------------------------------------
# 3. cells/ must not cross-import other cells' internal/
# ---------------------------------------------------------------
header "Check 3: cells/ cross-import isolation"

CROSS_CELL_VIOLATIONS=""
for CELL_DIR in "${SRC_DIR}"/cells/*/; do
    CELL_NAME=$(basename "${CELL_DIR}")
    # Find imports of other cells' packages (not self)
    OTHER_CELLS=$(grep -rn '"github.com/ghbvf/gocell/cells/' "${CELL_DIR}" --include='*.go' 2>/dev/null \
        | grep -v "gocell/cells/${CELL_NAME}/" || true)
    if [ -n "${OTHER_CELLS}" ]; then
        CROSS_CELL_VIOLATIONS="${CROSS_CELL_VIOLATIONS}${OTHER_CELLS}\n"
    fi
done

if [ -n "${CROSS_CELL_VIOLATIONS}" ]; then
    red "FAIL: cells/ has cross-cell imports:"
    printf '%b' "${CROSS_CELL_VIOLATIONS}"
    FAIL=1
else
    green "PASS: cells/ has no cross-cell imports"
fi

# ---------------------------------------------------------------
# 4. go.mod direct dependency whitelist
# ---------------------------------------------------------------
header "Check 4: go.mod direct dependency whitelist"

ALLOWED_DEPS=(
    "github.com/fsnotify/fsnotify"
    "github.com/go-chi/chi/v5"
    "github.com/golang-jwt/jwt/v5"
    "github.com/jackc/pgx/v5"
    "github.com/rabbitmq/amqp091-go"
    "github.com/redis/go-redis/v9"
    "github.com/stretchr/testify"
    "golang.org/x/crypto"
    "gopkg.in/yaml.v3"
    "nhooyr.io/websocket"
)

# Extract direct (non-indirect) require lines from go.mod
DIRECT_DEPS=$(sed -n '/^require (/,/^)/p' "${SRC_DIR}/go.mod" \
    | grep -v '// indirect' \
    | grep -v '^require' \
    | grep -v '^)' \
    | awk '{print $1}' \
    | sort)

GOMOD_VIOLATIONS=""
while IFS= read -r dep; do
    [ -z "${dep}" ] && continue
    FOUND=0
    for allowed in "${ALLOWED_DEPS[@]}"; do
        if [ "${dep}" = "${allowed}" ]; then
            FOUND=1
            break
        fi
    done
    if [ "${FOUND}" -eq 0 ]; then
        GOMOD_VIOLATIONS="${GOMOD_VIOLATIONS}  ${dep}\n"
    fi
done <<< "${DIRECT_DEPS}"

if [ -n "${GOMOD_VIOLATIONS}" ]; then
    red "FAIL: go.mod has unlisted direct dependencies:"
    printf '%b' "${GOMOD_VIOLATIONS}"
    FAIL=1
else
    green "PASS: go.mod direct dependencies match whitelist"
fi

# ---------------------------------------------------------------
# 5. Banned field names in YAML metadata
# ---------------------------------------------------------------
header "Check 5: Banned field names in YAML metadata"

BANNED_FIELDS=(
    "cellId"
    "sliceId"
    "contractId"
    "assemblyId"
    "ownedSlices"
    "authoritativeData"
    "producer"
    "consumers"
    "callsContracts"
    "publishes"
    "consumes"
)

BANNED_PATTERN=$(IFS='|'; echo "${BANNED_FIELDS[*]}")

YAML_VIOLATIONS=$(grep -rn "^\s*\(${BANNED_PATTERN}\)\s*:" "${REPO_ROOT}" \
    --include='*.yaml' --include='*.yml' \
    --exclude-dir='.git' \
    --exclude-dir='node_modules' \
    --exclude-dir='vendor' \
    2>/dev/null || true)

if [ -n "${YAML_VIOLATIONS}" ]; then
    red "FAIL: Banned field names found in YAML:"
    echo "${YAML_VIOLATIONS}"
    FAIL=1
else
    green "PASS: No banned field names in YAML metadata"
fi

# ---------------------------------------------------------------
# Summary
# ---------------------------------------------------------------
header "Summary"

if [ "${FAIL}" -eq 0 ]; then
    green "All KG verification checks passed."
    exit 0
else
    red "One or more KG verification checks failed."
    exit 1
fi
