package contractgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/metadata"
)

// COMMAND-CONTRACT-CONSISTENCY-LEVEL-01 (#1668) — contractgen codegen funnel tests.
//
// These tests pin the Hard main gate: a kind=command contract whose
// consistencyLevel < L1 (i.e. L0 LocalOnly) must produce a types_gen.go that
// does not compile (uint underflow), so an L0 command contract cannot exist in
// a buildable tree. A command crosses the local boundary and needs at least
// single-cell transactional atomicity (L1 LocalTx); L0 is structurally
// inapplicable. The governance rule (kernel/governance) is the Medium backstop
// for codegen:false contracts and in-memory ProjectMeta fixtures.
//
// Mirror of projection_consistency_test.go (floor L3); here the floor is L1.

// parseSynthCommandProject parses the on-disk synth_command fixture (valid
// schemaRefs) so consistency-level tests can override the level in-memory and
// still drive buildContractSpec past the schemaRef gate.
func parseSynthCommandProject(t *testing.T) (string, *metadata.ProjectMeta) {
	t.Helper()
	absTestDir, err := filepath.Abs(filepath.Join("testdata", "synth", "synth_command"))
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}
	p, err := metadata.NewParser(absTestDir).Parse()
	if err != nil {
		t.Fatalf("parse synth_command fixture: %v", err)
	}
	return absTestDir, p
}

const synthCommandContractID = "command.synth.do.v1"

// TestCommandTypesEmitsConsistencyGuard verifies the generated types_gen.go for
// a kind=command contract carries the compile-time level guard referencing
// cellvocab, parameterized by the contract's own level (>= L1).
func TestCommandTypesEmitsConsistencyGuard(t *testing.T) {
	t.Parallel()
	for _, level := range []string{"L1", "L2", "L3", "L4"} {
		spec := &ContractGenSpec{
			PackageName:      "do",
			PackagePath:      "generated/contracts/command/synth/do/v1",
			ContractID:       synthCommandContractID,
			Kind:             "command",
			ConsistencyLevel: level,
			SourceFile:       "contracts/command/synth/do/v1/contract.yaml",
		}
		out, err := renderTypes(spec)
		if err != nil {
			t.Fatalf("renderTypes(%s): %v", level, err)
		}
		got := string(out)
		if !strings.Contains(got, `"github.com/ghbvf/gocell/framework/kernel/cellvocab"`) {
			t.Errorf("level %s: generated types missing cellvocab import:\n%s", level, got)
		}
		wantGuard := "const _ = uint(cellvocab." + level + " - cellvocab.L1)"
		if !strings.Contains(got, wantGuard) {
			t.Errorf("level %s: generated types missing guard %q:\n%s", level, wantGuard, got)
		}
	}
}

// TestCommandTypesEmitsGuardForLowLevel locks that the template emits the
// overflow form for the sub-L1 level (L0): `uint(cellvocab.L0 - cellvocab.L1)`
// whose constant operand is negative — the form that fails to compile.
func TestCommandTypesEmitsGuardForLowLevel(t *testing.T) {
	t.Parallel()
	spec := &ContractGenSpec{
		PackageName:      "do",
		ContractID:       synthCommandContractID,
		Kind:             "command",
		ConsistencyLevel: "L0",
		SourceFile:       "contracts/command/synth/do/v1/contract.yaml",
	}
	out, err := renderTypes(spec)
	if err != nil {
		t.Fatalf("renderTypes(L0): %v", err)
	}
	wantGuard := "const _ = uint(cellvocab.L0 - cellvocab.L1)"
	if !strings.Contains(string(out), wantGuard) {
		t.Errorf("L0: generated types missing overflow guard %q:\n%s", wantGuard, out)
	}
}

// TestBuildCommandLowLevelAccepted locks a deliberate design choice:
// buildContractSpec does NOT reject L0 for command contracts — L0 parses, so
// the builder lets it through and the compile-time guard in types_gen.go (uint
// overflow) is the sole enforcement point. If a future change adds a `>= L1`
// floor check in the builder, this test fails on purpose: a builder-time check
// is a Medium guard (ContractMeta is open; in-memory/codegen=false vectors
// bypass it), which would silently degrade the Hard compile-time gate. Move the
// floor check, don't duplicate it.
func TestBuildCommandLowLevelAccepted(t *testing.T) {
	t.Parallel()
	root, p := parseSynthCommandProject(t)
	p.Contracts[synthCommandContractID].ConsistencyLevel = "L0"
	if _, err := buildContractSpec(root, p, synthCommandContractID); err != nil {
		t.Errorf("L0 command: buildContractSpec must accept parseable level "+
			"(floor is the compile-time guard, not the builder); got error: %v", err)
	}
}

// TestCommandLowLevelGuardFailsCompile is the Hard proof: a kind=command spec at
// L0 renders a types_gen.go that fails to compile. This makes an L0 command
// contract unrepresentable in a buildable tree.
func TestCommandLowLevelGuardFailsCompile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping go-build compile guard in -short mode")
	}
	root := repoRoot(t)
	spec := &ContractGenSpec{
		PackageName:      "do",
		PackagePath:      "generated/contracts/command/synth/do/v1",
		ContractID:       synthCommandContractID,
		Kind:             "command",
		ConsistencyLevel: "L0", // below the L1 floor — must not compile
		SourceFile:       "contracts/command/synth/do/v1/contract.yaml",
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
	if data, rerr := os.ReadFile(filepath.Join(root, "framework", "go.mod")); rerr == nil {
		for _, ln := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(ln, "go ") {
				goDirective = strings.TrimSpace(ln)
				break
			}
		}
	}
	gomod := "module commandguardcompile\n\n" + goDirective +
		"\n\nrequire github.com/ghbvf/gocell/framework v0.0.0\n\nreplace github.com/ghbvf/gocell/framework => " + root + "/framework\n"
	writeFile("go.mod", []byte(gomod))

	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = dir
	out2, berr := cmd.CombinedOutput()
	if berr == nil {
		t.Fatalf("expected L0 command types_gen.go to FAIL compilation "+
			"(COMMAND-CONTRACT-CONSISTENCY-LEVEL-01 Hard gate), but it compiled:\n%s", out2)
	}
	if !strings.Contains(string(out2), "overflows uint") {
		t.Errorf("expected uint-overflow compile error, got:\n%s", out2)
	}
}

// TestBuildCommandInvalidLevel verifies buildContractSpec returns a clean build
// error for a command contract whose consistencyLevel is empty or unparseable,
// rather than emitting syntactically broken Go (cellvocab.<garbage>).
func TestBuildCommandInvalidLevel(t *testing.T) {
	t.Parallel()
	for _, level := range []string{"", "L9", "garbage"} {
		root, p := parseSynthCommandProject(t)
		p.Contracts[synthCommandContractID].ConsistencyLevel = level
		if _, err := buildContractSpec(root, p, synthCommandContractID); err == nil {
			t.Errorf("level %q: expected buildContractSpec error for invalid command level, got nil", level)
		}
	}
}
