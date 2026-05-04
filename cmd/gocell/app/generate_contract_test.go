package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGenerateContract_DryRunVerifyMutex verifies --dry-run + --verify are mutually exclusive.
func TestGenerateContract_DryRunVerifyMutex(t *testing.T) {
	t.Parallel()
	err := generateContract([]string{"--dry-run", "--verify", "some.contract.v1"})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutex error, got %v", err)
	}
}

// TestGenerateContract_UnknownFlag asserts that an unknown flag returns an error.
func TestGenerateContract_UnknownFlag(t *testing.T) {
	t.Parallel()
	err := generateContract([]string{"--no-such-flag"})
	if err == nil {
		t.Fatal("expected error from flag parser")
	}
}

// NB: TestGenerateContract_NoArgs / _AllAndPositionalMutex / _DryRunOnlyFlag /
// _VerifyOnlyFlag deleted in K#05 W2 — `--all` now defaults to true and
// positional ids beat the default flag (no mutex error). The new flag
// semantics are covered by codegen_cmd_test.go's table-driven cases for
// both `cell` and `contract` (parseCodegenFlags is shared).

// minimalCodegenContractProject creates a minimal project with one contract
// that has codegen=true. Returns root and the contract id.
func minimalCodegenContractProject(t *testing.T) (root, contractID string) {
	t.Helper()
	root = t.TempDir()
	contractID = "http.order.create.v1"

	if err := os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module example.com/contractgentest\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	// contracts/http/order/create/v1/contract.yaml
	contractDir := filepath.Join(root, "contracts", "http", "order", "create", "v1")
	if err := os.MkdirAll(contractDir, 0o755); err != nil {
		t.Fatalf("mkdir contracts: %v", err)
	}

	contractYAML := `id: http.order.create.v1
kind: http
codegen: true
endpoints:
  http:
    method: POST
    path: /api/v1/orders
    successStatus: 201
schemaRefs:
  request: request.schema.json
  response: response.schema.json
`
	if err := os.WriteFile(filepath.Join(contractDir, "contract.yaml"), []byte(contractYAML), 0o644); err != nil {
		t.Fatalf("write contract.yaml: %v", err)
	}

	// Minimal JSON schemas
	requestSchema := `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "title": "CreateOrderRequest",
  "type": "object",
  "properties": {
    "name": {"type": "string"}
  },
  "required": ["name"]
}`
	if err := os.WriteFile(filepath.Join(contractDir, "request.schema.json"), []byte(requestSchema), 0o644); err != nil {
		t.Fatalf("write request.schema.json: %v", err)
	}

	responseSchema := `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "title": "CreateOrderResponse",
  "type": "object",
  "properties": {
    "id": {"type": "string"}
  }
}`
	if err := os.WriteFile(filepath.Join(contractDir, "response.schema.json"), []byte(responseSchema), 0o644); err != nil {
		t.Fatalf("write response.schema.json: %v", err)
	}

	return root, contractID
}

// TestGenerateContract_SuccessAll creates a minimal fake project in a temp dir
// and invokes generateContract(["--all"]), asserting the generated files are written.
func TestGenerateContract_SuccessAll(t *testing.T) {
	// Not parallel: uses os.Chdir which is process-global.
	// K#04 uses the same pattern (see TestGenerateCell_SuccessPath in generate_cell_test.go).
	root, _ := minimalCodegenContractProject(t)

	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	if err := generateContract([]string{"--all"}); err != nil {
		t.Fatalf("generateContract --all returned error: %v", err)
	}

	// Verify types_gen.go was written.
	typesGenFile := filepath.Join(root, "generated", "contracts", "http", "order", "create", "v1", "types_gen.go")
	if _, err := os.Stat(typesGenFile); err != nil {
		t.Fatalf("expected types_gen.go to be written at %s: %v", typesGenFile, err)
	}
}

// TestGenerateContract_SuccessPositionalID calls with a specific contract id.
func TestGenerateContract_SuccessPositionalID(t *testing.T) {
	// Not parallel: uses os.Chdir which is process-global.
	root, contractID := minimalCodegenContractProject(t)

	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	if err := generateContract([]string{contractID}); err != nil {
		t.Fatalf("generateContract %s returned error: %v", contractID, err)
	}

	typesGenFile := filepath.Join(root, "generated", "contracts", "http", "order", "create", "v1", "types_gen.go")
	if _, err := os.Stat(typesGenFile); err != nil {
		t.Fatalf("expected types_gen.go at %s: %v", typesGenFile, err)
	}
}

// TestGenerateContract_DryRunDoesNotWrite verifies --dry-run does not write files.
func TestGenerateContract_DryRunDoesNotWrite(t *testing.T) {
	// Not parallel: uses os.Chdir which is process-global.
	root, _ := minimalCodegenContractProject(t)

	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	if err := generateContract([]string{"--all", "--dry-run"}); err != nil {
		t.Fatalf("generateContract --all --dry-run returned error: %v", err)
	}

	typesGenFile := filepath.Join(root, "generated", "contracts", "http", "order", "create", "v1", "types_gen.go")
	if _, err := os.Stat(typesGenFile); err == nil {
		t.Fatal("expected types_gen.go NOT to be written in dry-run mode, but it exists")
	}
}

// TestGenerateContract_VerifyNoDrift verifies that --verify passes when generated
// files are already up to date.
func TestGenerateContract_VerifyNoDrift(t *testing.T) {
	// Not parallel: uses os.Chdir which is process-global.
	root, _ := minimalCodegenContractProject(t)

	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	// First generate to produce up-to-date files.
	if err := generateContract([]string{"--all"}); err != nil {
		t.Fatalf("initial generateContract --all failed: %v", err)
	}

	// Now verify — should pass.
	if err := generateContract([]string{"--all", "--verify"}); err != nil {
		t.Fatalf("generateContract --all --verify on fresh project returned error: %v", err)
	}
}

// TestGenerateContract_VerifyDetectsDrift verifies that --verify exits non-zero
// when generated files are missing (i.e. drifted).
func TestGenerateContract_VerifyDetectsDrift(t *testing.T) {
	// Not parallel: uses os.Chdir which is process-global.
	root, _ := minimalCodegenContractProject(t)

	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	// Do NOT pre-generate — verify should detect missing files as drift.
	err = generateContract([]string{"--all", "--verify"})
	if err == nil || !strings.Contains(err.Error(), "drift") {
		t.Fatalf("expected drift error, got %v", err)
	}
}

// TestGenerateContract_UnknownContractID verifies that specifying a non-existent
// contract id returns an error.
func TestGenerateContract_UnknownContractID(t *testing.T) {
	// Not parallel: uses os.Chdir which is process-global.
	root, _ := minimalCodegenContractProject(t)

	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	if err := generateContract([]string{"http.no.such.contract.v1"}); err == nil {
		t.Fatal("expected error for non-existent contract id")
	}
}

// TestGenerateContract_AllNoOptedIn verifies that --all on a project with no
// Codegen=true contracts returns no error and generates nothing.
func TestGenerateContract_AllNoOptedIn(t *testing.T) {
	// Not parallel: uses os.Chdir which is process-global.
	root := t.TempDir()

	if err := os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module example.com/emptyproject\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	// Contract with codegen=false (default).
	contractDir := filepath.Join(root, "contracts", "http", "ping", "v1")
	if err := os.MkdirAll(contractDir, 0o755); err != nil {
		t.Fatalf("mkdir contracts: %v", err)
	}
	contractYAML := "id: http.ping.v1\nkind: http\n"
	if err := os.WriteFile(filepath.Join(contractDir, "contract.yaml"), []byte(contractYAML), 0o644); err != nil {
		t.Fatalf("write contract.yaml: %v", err)
	}

	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })

	if err := generateContract([]string{"--all"}); err != nil {
		t.Fatalf("generateContract --all on project with no opted-in contracts returned error: %v", err)
	}
}
