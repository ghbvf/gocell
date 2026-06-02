package contractgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// PROJECTION-CONSISTENCY-01 (gh #960) — contractgen codegen funnel tests.
//
// These tests pin the Hard main gate: a kind=projection contract whose
// consistencyLevel < L3 must produce a types_gen.go that does not compile
// (uint underflow), so an invalid projection contract cannot exist in a
// buildable tree. The governance rule (kernel/governance) is the Medium
// backstop for codegen:false contracts and in-memory ProjectMeta fixtures.

// TestProjectionTypesEmitsConsistencyGuard verifies the generated types_gen.go
// for a kind=projection contract carries the compile-time level guard
// referencing cellvocab, parameterized by the contract's own level.
func TestProjectionTypesEmitsConsistencyGuard(t *testing.T) {
	t.Parallel()
	for _, level := range []string{"L3", "L4"} {
		spec := &ContractGenSpec{
			PackageName:      "statussummary",
			PackagePath:      "generated/contracts/projection/order/status-summary/v1",
			ContractID:       "projection.order.status-summary.v1",
			Kind:             "projection",
			ConsistencyLevel: level,
			SourceFile:       "examples/todoorder/contracts/projection/order/status-summary/v1/contract.yaml",
		}
		out, err := renderTypes(spec)
		if err != nil {
			t.Fatalf("renderTypes(%s): %v", level, err)
		}
		got := string(out)
		if !strings.Contains(got, `"github.com/ghbvf/gocell/kernel/cellvocab"`) {
			t.Errorf("level %s: generated types missing cellvocab import:\n%s", level, got)
		}
		wantGuard := "const _ = uint(cellvocab." + level + " - cellvocab.L3)"
		if !strings.Contains(got, wantGuard) {
			t.Errorf("level %s: generated types missing guard %q:\n%s", level, wantGuard, got)
		}
	}
}

// TestProjectionLowLevelGuardFailsCompile is the Hard proof: a kind=projection
// spec at L2 renders a types_gen.go that fails to compile. This makes an
// invalid projection contract unrepresentable in a buildable tree.
func TestProjectionLowLevelGuardFailsCompile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping go-build compile guard in -short mode")
	}
	root := repoRoot(t)
	spec := &ContractGenSpec{
		PackageName:      "statussummary",
		PackagePath:      "generated/contracts/projection/order/status-summary/v1",
		ContractID:       "projection.order.status-summary.v1",
		Kind:             "projection",
		ConsistencyLevel: "L2", // below the L3 floor — must not compile
		SourceFile:       "examples/todoorder/contracts/projection/order/status-summary/v1/contract.yaml",
	}
	out, err := renderTypes(spec)
	if err != nil {
		t.Fatalf("renderTypes: %v", err)
	}

	dir := t.TempDir()
	writeFile := func(name string, content []byte) {
		// #nosec G703 G304 -- test-internal: dir is t.TempDir(), name is a literal.
		if werr := os.WriteFile(filepath.Join(dir, name), content, 0o644); werr != nil {
			t.Fatalf("write %s: %v", name, werr)
		}
	}
	writeFile("types_gen.go", out)

	goDirective := "go 1.25"
	// #nosec G304 -- test-internal: root is the repo module root, not user input.
	if data, rerr := os.ReadFile(filepath.Join(root, "go.mod")); rerr == nil {
		for _, ln := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(ln, "go ") {
				goDirective = strings.TrimSpace(ln)
				break
			}
		}
	}
	gomod := "module projectionguardcompile\n\n" + goDirective +
		"\n\nrequire github.com/ghbvf/gocell v0.0.0\n\nreplace github.com/ghbvf/gocell => " + root + "\n"
	writeFile("go.mod", []byte(gomod))

	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = dir
	out2, berr := cmd.CombinedOutput()
	if berr == nil {
		t.Fatalf("expected L2 projection types_gen.go to FAIL compilation (PROJECTION-CONSISTENCY-01 Hard gate), but it compiled:\n%s", out2)
	}
	if !strings.Contains(string(out2), "overflows uint") {
		t.Errorf("expected uint-overflow compile error, got:\n%s", out2)
	}
}

// TestBuildContractSpecProjectionInvalidLevel verifies buildContractSpec returns
// a clean build error for a projection contract whose consistencyLevel is empty
// or unparseable, rather than emitting syntactically broken Go.
func TestBuildContractSpecProjectionInvalidLevel(t *testing.T) {
	t.Parallel()
	for _, level := range []string{"", "L9", "garbage"} {
		root, p := setupHTTPMinimalRoot(t)
		id := "projection.order.status-summary.v1"
		p.Contracts[id] = &metadata.ContractMeta{
			ID:               id,
			Kind:             "projection",
			ConsistencyLevel: level,
			Lifecycle:        "active",
			Codegen:          true,
			Endpoints: metadata.EndpointsMeta{
				Provider: "ordercell",
				Readers:  []string{"edge-bff"},
			},
			File: "examples/todoorder/contracts/projection/order/status-summary/v1/contract.yaml",
		}
		if _, err := buildContractSpec(root, p, id); err == nil {
			t.Errorf("level %q: expected buildContractSpec error for invalid projection level, got nil", level)
		}
	}
}
