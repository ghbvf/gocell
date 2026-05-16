package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/codegen/contractgen"
)

// preRenderContracts generates all opted-in contracts for the project at root.
func preRenderContracts(t *testing.T, root string) {
	t.Helper()
	project, err := metadata.NewParser(root).Parse()
	if err != nil {
		t.Fatalf("pre-render metadata parse: %v", err)
	}
	if _, err := contractgen.Generate(root, project, contractgen.Options{Scope: contractgen.ScopeAll{}, Verify: false}); err != nil {
		t.Fatalf("pre-render generateAllContracts: %v", err)
	}
}

// minimalCodegenContractProjectClean creates a minimal project with one
// Codegen=true contract and pre-renders the generated files, leaving the
// project in a "verify clean" state. Used as the happy-path baseline.
func minimalCodegenContractProjectClean(t *testing.T) string {
	t.Helper()
	root, _ := minimalCodegenContractProject(t)

	// Pre-render to produce up-to-date generated files.
	preRenderContracts(t, root)
	return root
}

// --- flag-parsing tests (cheap, parallel) ------------------------------------

func TestVerifyCodegenContract_UnknownFlag(t *testing.T) {
	t.Parallel()
	if err := verifyCodegenContract(context.Background(), []string{"--bogus"}); err == nil {
		t.Fatal("expected flag-parse error")
	}
}

// --- in-place mode (--local) -------------------------------------------------

func TestVerifyCodegenContract_LocalNoDrift(t *testing.T) {
	// Not parallel: uses os.Chdir which is process-global.
	root := minimalCodegenContractProjectClean(t)
	chdirToRoot(t, root)
	if err := verifyCodegenContract(context.Background(), []string{"--local"}); err != nil {
		t.Fatalf("verifyCodegenContract --local on clean project: %v", err)
	}
}

func TestVerifyCodegenContract_LocalDriftWhenGenFileMissing(t *testing.T) {
	// Not parallel: uses os.Chdir which is process-global.
	root := minimalCodegenContractProjectClean(t)

	// Remove types_gen.go to simulate "contract.yaml changed but generate not run".
	typesGenFile := filepath.Join(root, "generated", "contracts", "http", "order", "create", "v1", "types_gen.go")
	if err := os.Remove(typesGenFile); err != nil {
		t.Fatalf("remove types_gen.go: %v", err)
	}

	chdirToRoot(t, root)
	err := verifyCodegenContract(context.Background(), []string{"--local"})
	if err == nil || !strings.Contains(err.Error(), "drift") {
		t.Fatalf("expected drift error, got %v", err)
	}
}

func TestVerifyCodegenContract_LocalNoProjectFails(t *testing.T) {
	// Not parallel: uses os.Chdir which is process-global.
	// An empty temp dir without go.mod fails before reaching contractgen.
	root := t.TempDir()
	chdirToRoot(t, root)
	if err := verifyCodegenContract(context.Background(), []string{"--local"}); err == nil {
		t.Fatal("expected error when no project root")
	}
}

// --- sandbox mode (requires git) ---------------------------------------------

// TestVerifyCodegenContract_SandboxNoDrift exercises sandbox mode (--local=false)
// against a tmp git repo whose HEAD already contains clean generated files.
// Requires git in PATH.
//
// Note: sandbox mode is complex to unit-test hermetically (it invokes git
// worktree and re-runs code generation inside the sandbox). Full coverage of
// this path is also owned by hack/verify-codegen-contract.sh in CI.
func TestVerifyCodegenContract_SandboxNoDrift(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := minimalCodegenContractProjectClean(t)
	gitInit(t, root)
	chdirToRoot(t, root)

	if err := verifyCodegenContract(context.Background(), []string{"--local=false"}); err != nil {
		t.Fatalf("verifyCodegenContract sandbox on clean repo: %v", err)
	}
}
