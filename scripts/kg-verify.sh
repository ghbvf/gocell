#!/usr/bin/env bash
# kg-verify.sh — Knowledge-Graph integrity checks for GoCell (T83-T90)
#
# Usage:
#   ./scripts/kg-verify.sh [src-root]
#
# Exit code 0 = all checks pass, non-zero = at least one failure.

set -euo pipefail

ROOT="${1:-$(cd "$(dirname "$0")/.." && pwd)/src}"
FAIL=0
CHECK=0

pass() { CHECK=$((CHECK+1)); echo "  [PASS] C-$(printf '%02d' $CHECK): $1"; }
fail() { CHECK=$((CHECK+1)); FAIL=$((FAIL+1)); echo "  [FAIL] C-$(printf '%02d' $CHECK): $1"; }

echo "=== GoCell KG Verification ==="
echo "Root: $ROOT"
echo ""

# ---------------------------------------------------------------------------
# C-01..C-03: cell.yaml required fields (T83)
# ---------------------------------------------------------------------------
echo "--- Cell/Slice/Contract YAML checks ---"

for cell_yaml in "$ROOT"/cells/*/cell.yaml; do
    [ -f "$cell_yaml" ] || continue
    cell_id=$(basename "$(dirname "$cell_yaml")")

    # Check required fields: id, type, consistencyLevel, owner, schema.primary, verify.smoke
    for field in "id:" "type:" "consistencyLevel:" "owner:" "schema:" "verify:"; do
        if ! grep -q "$field" "$cell_yaml" 2>/dev/null; then
            fail "cell.yaml ($cell_id) missing field: $field"
            continue 2
        fi
    done
    pass "cell.yaml ($cell_id) has all required fields"
done

# C-04..C-06: slice.yaml required fields
for slice_yaml in "$ROOT"/cells/*/slices/*/slice.yaml; do
    [ -f "$slice_yaml" ] || continue
    slice_id=$(basename "$(dirname "$slice_yaml")")

    for field in "id:" "belongsToCell:" "contractUsages:" "verify:"; do
        if ! grep -q "$field" "$slice_yaml" 2>/dev/null; then
            fail "slice.yaml ($slice_id) missing field: $field"
            continue 2
        fi
    done
    pass "slice.yaml ($slice_id) has required fields"
done

# C-07..C-08: contract.yaml required fields
for contract_yaml in $(find "$ROOT"/contracts -name "contract.yaml" 2>/dev/null); do
    [ -f "$contract_yaml" ] || continue
    rel_path="${contract_yaml#$ROOT/}"

    for field in "id:" "kind:" "ownerCell:" "consistencyLevel:"; do
        if ! grep -q "$field" "$contract_yaml" 2>/dev/null; then
            fail "contract.yaml ($rel_path) missing field: $field"
            continue 2
        fi
    done
    pass "contract.yaml ($rel_path) has required fields"
done

echo ""

# ---------------------------------------------------------------------------
# C-09: go build (T85)
# ---------------------------------------------------------------------------
echo "--- Build & Vet checks ---"

if (cd "$ROOT" && go build ./... 2>&1); then
    pass "go build ./... succeeded"
else
    fail "go build ./... failed"
fi

# C-10: go vet (T85)
if (cd "$ROOT" && go vet ./... 2>&1); then
    pass "go vet ./... succeeded"
else
    fail "go vet ./... failed"
fi

echo ""

# ---------------------------------------------------------------------------
# C-11: Layer isolation — adapters must not import cells (T86)
# ---------------------------------------------------------------------------
echo "--- Layer isolation checks ---"

if grep -r "gocell/cells" "$ROOT/adapters/" 2>/dev/null | grep -v "_test.go" | grep -v ".gitkeep" | grep -q .; then
    fail "adapters/ imports cells/ (layer violation)"
else
    pass "adapters/ does not import cells/"
fi

# C-12: kernel must not import runtime, adapters, or cells
if grep -r "gocell/runtime\|gocell/adapters\|gocell/cells" "$ROOT/kernel/" 2>/dev/null | grep -v "_test.go" | grep -q .; then
    fail "kernel/ imports runtime/, adapters/, or cells/ (layer violation)"
else
    pass "kernel/ does not import runtime/, adapters/, or cells/"
fi

# C-13: runtime must not import cells or adapters
if grep -r "gocell/cells\|gocell/adapters" "$ROOT/runtime/" 2>/dev/null | grep -v "_test.go" | grep -q .; then
    fail "runtime/ imports cells/ or adapters/ (layer violation)"
else
    pass "runtime/ does not import cells/ or adapters/"
fi

echo ""

# ---------------------------------------------------------------------------
# C-14: Adapter Close method check (T87)
# ---------------------------------------------------------------------------
echo "--- Adapter Close method checks ---"

for adapter_dir in "$ROOT"/adapters/*/; do
    [ -d "$adapter_dir" ] || continue
    adapter_name=$(basename "$adapter_dir")

    # Check if there are any non-test .go files (besides doc.go)
    go_files=$(find "$adapter_dir" -maxdepth 1 -name "*.go" ! -name "*_test.go" ! -name "doc.go" 2>/dev/null)
    if [ -z "$go_files" ]; then
        pass "adapter/$adapter_name has no implementation yet (Close check deferred)"
    elif grep -l "func.*Close" $go_files >/dev/null 2>&1; then
        pass "adapter/$adapter_name has Close method"
    else
        fail "adapter/$adapter_name missing Close method"
    fi
done

echo ""

# ---------------------------------------------------------------------------
# C-15: go.mod dependency whitelist check (T88)
# ---------------------------------------------------------------------------
echo "--- go.mod checks ---"

GOMOD="$ROOT/go.mod"
if [ -f "$GOMOD" ]; then
    # Check module name
    if grep -q "^module github.com/ghbvf/gocell" "$GOMOD"; then
        pass "go.mod module name is correct"
    else
        fail "go.mod module name is incorrect"
    fi

    # Check for known problematic dependencies
    BLOCKED_DEPS="gorm.io database/sql/driver github.com/gin-gonic github.com/gorilla/mux"
    blocked_found=0
    for dep in $BLOCKED_DEPS; do
        if grep -q "$dep" "$GOMOD" 2>/dev/null; then
            fail "go.mod contains blocked dependency: $dep"
            blocked_found=1
        fi
    done
    if [ $blocked_found -eq 0 ]; then
        pass "go.mod has no blocked dependencies"
    fi
else
    fail "go.mod not found"
fi

echo ""

# ---------------------------------------------------------------------------
# C-16..C-17: errcode prefix check (T89)
# ---------------------------------------------------------------------------
echo "--- errcode checks ---"

# Check that adapter code uses errcode (not bare errors.New) if any .go files exist
adapter_go_files=$(find "$ROOT/adapters" -name "*.go" ! -name "*_test.go" ! -name "doc.go" 2>/dev/null)
if [ -z "$adapter_go_files" ]; then
    pass "adapter errcode check: no implementation files yet (deferred)"
else
    if echo "$adapter_go_files" | xargs grep -l 'errors\.New(' 2>/dev/null | grep -q .; then
        fail "adapter code uses bare errors.New instead of errcode"
    else
        pass "adapter code does not use bare errors.New"
    fi
fi

# Check that errcode constants use consistent prefixes
if [ -f "$ROOT/pkg/errcode/errcode.go" ]; then
    if grep -q "ERR_" "$ROOT/pkg/errcode/errcode.go"; then
        pass "errcode uses ERR_ prefix convention"
    else
        fail "errcode does not use ERR_ prefix convention"
    fi
else
    fail "pkg/errcode/errcode.go not found"
fi

echo ""

# ---------------------------------------------------------------------------
# C-18..C-25: Prohibited field names in YAML (T90)
# ---------------------------------------------------------------------------
echo "--- Prohibited field name checks ---"

PROHIBITED_FIELDS="cellId sliceId contractId assemblyId ownedSlices authoritativeData producer: consumers: callsContracts publishes: consumes:"
yaml_files=$(find "$ROOT" -name "*.yaml" -not -path "*/generated/*" -not -path "*/.git/*" 2>/dev/null)

prohibited_found=0
for field in $PROHIBITED_FIELDS; do
    # Strip trailing colon for display but search with it for accuracy
    display_field="${field%:}"
    search_field="$field"

    matches=$(echo "$yaml_files" | xargs grep -l "^[[:space:]]*${search_field}" 2>/dev/null || true)
    if [ -n "$matches" ]; then
        fail "Prohibited field '$display_field' found in: $matches"
        prohibited_found=1
    fi
done

if [ $prohibited_found -eq 0 ]; then
    pass "No prohibited field names found in YAML files"
fi

# Check that dynamic delivery fields are not in cell/slice/contract/assembly YAML
DYNAMIC_FIELDS="readiness: risk: blocker: done: verified: nextAction: updatedAt:"
meta_yaml_files=$(find "$ROOT/cells" "$ROOT/contracts" "$ROOT/assemblies" -name "*.yaml" 2>/dev/null | grep -v "status-board")

dynamic_found=0
for field in $DYNAMIC_FIELDS; do
    display_field="${field%:}"
    matches=$(echo "$meta_yaml_files" | xargs grep -l "^[[:space:]]*${field}" 2>/dev/null || true)
    if [ -n "$matches" ]; then
        fail "Dynamic field '$display_field' found outside status-board: $matches"
        dynamic_found=1
    fi
done

if [ $dynamic_found -eq 0 ]; then
    pass "No dynamic delivery fields in cell/slice/contract/assembly YAML"
fi

echo ""

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
echo "=== Summary ==="
echo "Checks run: $CHECK"
echo "Passed:     $((CHECK - FAIL))"
echo "Failed:     $FAIL"

if [ $FAIL -gt 0 ]; then
    echo ""
    echo "KG verification FAILED."
    exit 1
else
    echo ""
    echo "KG verification PASSED."
    exit 0
fi
