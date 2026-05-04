package contractgen

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// --- helpers ------------------------------------------------------------------

// copyDirIntoTemp copies the entire directory tree under src into dst,
// preserving the subtree structure.
func copyDirIntoTemp(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			return relErr
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
	if err != nil {
		t.Fatalf("copyDirIntoTemp %s → %s: %v", src, dst, err)
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src) //nolint:gosec // test copies its own committed fixture files
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dst) //nolint:gosec // test creates its own tmp file
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	if closeErr := out.Close(); closeErr != nil && copyErr == nil {
		return closeErr
	}
	return copyErr
}

// synthHTTPMinimalFixture returns the absolute path to the synth_http_minimal fixture.
func synthHTTPMinimalFixture(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("testdata", "synth", "synth_http_minimal"))
	if err != nil {
		t.Fatalf("abs path synth_http_minimal: %v", err)
	}
	return abs
}

// synthEventFixture returns the absolute path to the synth_event fixture.
func synthEventFixture(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("testdata", "synth", "synth_event"))
	if err != nil {
		t.Fatalf("abs path synth_event: %v", err)
	}
	return abs
}

// setupHTTPMinimalRoot copies the synth_http_minimal fixture into a fresh
// t.TempDir(), adds a go.mod, parses it, and returns (root, project).
// Schema paths in the ProjectMeta are relative to root, so BuildContractSpec
// and codegen.Write both use the same root. Generated files land in
// root/generated/... and do not pollute committed testdata.
func setupHTTPMinimalRoot(t *testing.T) (string, *metadata.ProjectMeta) {
	t.Helper()
	fixture := synthHTTPMinimalFixture(t)
	root := t.TempDir()
	copyDirIntoTemp(t, fixture, root)
	goMod := "module github.com/ghbvf/gocell\n\ngo 1.22\n"
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	p, err := metadata.NewParser(root).Parse()
	if err != nil {
		t.Fatalf("parse synth_http_minimal from tmp: %v", err)
	}
	return root, p
}

// setupEventRoot copies the synth_event fixture into a fresh t.TempDir()
// and parses it. Returns (root, project).
func setupEventRoot(t *testing.T) (string, *metadata.ProjectMeta) {
	t.Helper()
	fixture := synthEventFixture(t)
	root := t.TempDir()
	copyDirIntoTemp(t, fixture, root)
	goMod := "module github.com/ghbvf/gocell\n\ngo 1.22\n"
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	p, err := metadata.NewParser(root).Parse()
	if err != nil {
		t.Fatalf("parse synth_event from tmp: %v", err)
	}
	return root, p
}

// mustGenerate is a helper that calls Generate and fails the test on error.
func mustGenerate(t *testing.T, root string, p *metadata.ProjectMeta, opts Options) Result {
	t.Helper()
	res, err := Generate(root, p, opts)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return res
}

// fileExists reports whether path exists on disk.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// --- Normal path tests -------------------------------------------------------

// TestGenerate_DryRun_HTTP verifies that DryRun mode populates Result.Generated
// but does not create files on disk. The ActionWouldWrite result surfaces to the
// CLI layer for printing — Generate itself does not print.
func TestGenerate_DryRun_HTTP(t *testing.T) {
	t.Parallel()
	root, p := setupHTTPMinimalRoot(t)

	res := mustGenerate(t, root, p, Options{DryRun: true})

	// DryRun should report paths in Generated (ActionWouldWrite → Generated).
	if len(res.Generated) == 0 {
		t.Error("DryRun: expected Generated to be non-empty")
	}
	// No file should be created on disk.
	for _, path := range res.Generated {
		if fileExists(path) {
			t.Errorf("DryRun: file should not exist: %s", path)
		}
	}
}

// TestGenerate_WriteMode_HTTP verifies that default (non-DryRun, non-Verify)
// mode creates files on disk with non-empty content.
func TestGenerate_WriteMode_HTTP(t *testing.T) {
	t.Parallel()
	root, p := setupHTTPMinimalRoot(t)

	res := mustGenerate(t, root, p, Options{})

	// HTTP contract → 3 files: types_gen.go, iface_gen.go, handler_gen.go.
	if len(res.Generated) != 3 {
		t.Errorf("expected 3 generated files, got %d: %v", len(res.Generated), res.Generated)
	}
	for _, path := range res.Generated {
		content, err := os.ReadFile(path) //nolint:gosec // test reads its own written tmp file
		if err != nil {
			t.Errorf("ReadFile %s: %v", path, err)
			continue
		}
		if len(content) == 0 {
			t.Errorf("generated file is empty: %s", path)
		}
		if !strings.Contains(string(content), "DO NOT EDIT") {
			t.Errorf("generated file missing header: %s", path)
		}
	}
}

// TestGenerate_WriteMode_Event verifies that event contracts produce 2 files
// (types_gen.go + iface_gen.go) but NOT handler_gen.go.
func TestGenerate_WriteMode_Event(t *testing.T) {
	t.Parallel()
	root, p := setupEventRoot(t)

	res := mustGenerate(t, root, p, Options{})

	// Event contract → 2 files only.
	if len(res.Generated) != 2 {
		t.Errorf("expected 2 generated files for event, got %d: %v", len(res.Generated), res.Generated)
	}
	for _, path := range res.Generated {
		if strings.HasSuffix(path, "handler_gen.go") {
			t.Errorf("event contract should not generate handler_gen.go, but got: %s", path)
		}
	}
}

// TestGenerate_IdempotentSecondRun verifies that a second Generate run on
// already-written files reports ActionUnchanged (paths still in Generated, not Drifted).
func TestGenerate_IdempotentSecondRun(t *testing.T) {
	t.Parallel()
	root, p := setupHTTPMinimalRoot(t)

	// First run: write files.
	mustGenerate(t, root, p, Options{})

	// Second run: should report unchanged (still in Generated), no Drifted.
	res2 := mustGenerate(t, root, p, Options{})
	if len(res2.Drifted) != 0 {
		t.Errorf("second run should not report drift, got: %v", res2.Drifted)
	}
	if len(res2.Generated) == 0 {
		t.Errorf("second run should still report Generated (unchanged), got empty")
	}
}

// TestGenerate_VerifyClean verifies that after writing, Verify mode finds no drift.
func TestGenerate_VerifyClean(t *testing.T) {
	t.Parallel()
	root, p := setupHTTPMinimalRoot(t)

	mustGenerate(t, root, p, Options{})

	res := mustGenerate(t, root, p, Options{Verify: true})
	if len(res.Drifted) != 0 {
		t.Errorf("verify after clean write should have no drift, got: %v", res.Drifted)
	}
}

// TestGenerate_VerifyDrift verifies that Verify mode detects tampered files.
func TestGenerate_VerifyDrift(t *testing.T) {
	t.Parallel()
	root, p := setupHTTPMinimalRoot(t)

	res1 := mustGenerate(t, root, p, Options{})

	// Tamper with one of the written files.
	tampered := false
	for _, path := range res1.Generated {
		if strings.HasSuffix(path, "types_gen.go") {
			content, err := os.ReadFile(path) //nolint:gosec // test reads its own tmp file
			if err != nil {
				t.Fatalf("read for tamper: %v", err)
			}
			modified := strings.Replace(string(content), "package", "// tampered\npackage", 1)
			if err := os.WriteFile(path, []byte(modified), 0o644); err != nil { //nolint:gosec // G306: test writes to tmp
				t.Fatalf("write tampered: %v", err)
			}
			tampered = true
			break
		}
	}
	if !tampered {
		t.Fatal("could not find types_gen.go to tamper")
	}

	res2 := mustGenerate(t, root, p, Options{Verify: true})
	if len(res2.Drifted) == 0 {
		t.Error("verify should detect drift after tamper")
	}
}

// TestGenerate_VerifyMissingFile verifies that Verify mode reports drift when
// a file is missing (never written).
func TestGenerate_VerifyMissingFile(t *testing.T) {
	t.Parallel()
	root, p := setupHTTPMinimalRoot(t)

	// Verify without writing first — all files are missing → all drifted.
	res := mustGenerate(t, root, p, Options{Verify: true})
	if len(res.Drifted) == 0 {
		t.Error("verify with no prior write should report drift (files missing)")
	}
}

// TestGenerate_OnlyContract_HTTP verifies that OnlyContract restricts
// generation to a single contract.
func TestGenerate_OnlyContract_HTTP(t *testing.T) {
	t.Parallel()
	root, p := setupHTTPMinimalRoot(t)

	res := mustGenerate(t, root, p, Options{OnlyContract: "http.order.ping.v1"})

	// Should still get 3 files for the HTTP contract.
	if len(res.Generated) != 3 {
		t.Errorf("expected 3 files for OnlyContract=http.order.ping.v1, got %d: %v", len(res.Generated), res.Generated)
	}
}

// TestGenerate_AllCodegenTrue_MultipleContracts verifies that multiple
// Codegen=true contracts are all processed.
func TestGenerate_AllCodegenTrue_MultipleContracts(t *testing.T) {
	t.Parallel()

	// Build a merged root containing both synth fixtures.
	httpRoot, httpProject := setupHTTPMinimalRoot(t)
	eventRoot, eventProject := setupEventRoot(t)

	// Create a fresh merged root with both contract trees.
	mergedRoot := t.TempDir()
	goMod := "module github.com/ghbvf/gocell\n\ngo 1.22\n"
	if err := os.WriteFile(filepath.Join(mergedRoot, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	// Copy both contract trees into mergedRoot.
	httpContracts := filepath.Join(httpRoot, "contracts")
	if _, err := os.Stat(httpContracts); err == nil {
		copyDirIntoTemp(t, httpContracts, filepath.Join(mergedRoot, "contracts"))
	}
	eventContracts := filepath.Join(eventRoot, "contracts")
	if _, err := os.Stat(eventContracts); err == nil {
		copyDirIntoTemp(t, eventContracts, filepath.Join(mergedRoot, "contracts"))
	}

	// Build merged ProjectMeta from the two parsed projects.
	// Both projects were parsed with their own tmpRoot, so contract.File is
	// already relative to that root (same directory structure as mergedRoot
	// since we copied in the same contracts/ subtree). Keep File unchanged.
	merged := &metadata.ProjectMeta{
		Contracts: make(map[string]*metadata.ContractMeta),
	}
	for id, c := range httpProject.Contracts {
		cp := *c
		merged.Contracts[id] = &cp
	}
	for id, c := range eventProject.Contracts {
		cp := *c
		merged.Contracts[id] = &cp
	}

	res, err := Generate(mergedRoot, merged, Options{})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}

	// http.order.ping.v1 → 3 files; event.item-created.v1 → 2 files → total 5.
	if len(res.Generated) != 5 {
		t.Errorf("expected 5 total generated files, got %d: %v", len(res.Generated), res.Generated)
	}
}

// --- Error path tests ---------------------------------------------------------

// TestGenerate_OnlyContract_NotFound verifies error when OnlyContract id
// does not exist in the project.
func TestGenerate_OnlyContract_NotFound(t *testing.T) {
	t.Parallel()
	p := &metadata.ProjectMeta{Contracts: map[string]*metadata.ContractMeta{}}
	_, err := Generate(t.TempDir(), p, Options{OnlyContract: "http.does.not.exist.v1"})
	if err == nil {
		t.Fatal("expected error for non-existent contract")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found', got: %v", err)
	}
}

// TestGenerate_OnlyContract_CodegenFalse verifies error when OnlyContract
// points to a contract with Codegen=false.
func TestGenerate_OnlyContract_CodegenFalse(t *testing.T) {
	t.Parallel()
	p := &metadata.ProjectMeta{
		Contracts: map[string]*metadata.ContractMeta{
			"http.foo.bar.v1": {
				ID:      "http.foo.bar.v1",
				Kind:    "http",
				Codegen: false,
			},
		},
	}
	_, err := Generate(t.TempDir(), p, Options{OnlyContract: "http.foo.bar.v1"})
	if err == nil {
		t.Fatal("expected error for Codegen=false contract")
	}
	if !strings.Contains(err.Error(), "codegen=false") {
		t.Errorf("error should mention 'codegen=false', got: %v", err)
	}
}

// TestGenerate_NilProject verifies that a nil project returns an error.
func TestGenerate_NilProject(t *testing.T) {
	t.Parallel()
	_, err := Generate(t.TempDir(), nil, Options{})
	if err == nil {
		t.Fatal("expected error for nil project")
	}
}

// TestGenerate_EmptyRoot verifies that an empty root returns an error.
func TestGenerate_EmptyRoot(t *testing.T) {
	t.Parallel()
	_, err := Generate("", &metadata.ProjectMeta{}, Options{})
	if err == nil {
		t.Fatal("expected error for empty root")
	}
}

// TestGenerate_BuildSpecError verifies that BuildContractSpec errors propagate.
func TestGenerate_BuildSpecError(t *testing.T) {
	t.Parallel()
	// kind=event with missing payload schemaRef will fail BuildContractSpec.
	p := &metadata.ProjectMeta{
		Contracts: map[string]*metadata.ContractMeta{
			"event.broken.v1": {
				ID:      "event.broken.v1",
				Kind:    "event",
				Codegen: true,
				// No SchemaRefs.Payload.
			},
		},
	}
	_, err := Generate(t.TempDir(), p, Options{})
	if err == nil {
		t.Fatal("expected error propagated from BuildContractSpec")
	}
}

// TestGenerate_NoCodegenContracts verifies that a project with no Codegen=true
// contracts produces an empty result without error.
func TestGenerate_NoCodegenContracts(t *testing.T) {
	t.Parallel()
	p := &metadata.ProjectMeta{
		Contracts: map[string]*metadata.ContractMeta{
			"http.foo.bar.v1": {
				ID:      "http.foo.bar.v1",
				Kind:    "http",
				Codegen: false,
			},
		},
	}
	res, err := Generate(t.TempDir(), p, Options{})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(res.Generated) != 0 {
		t.Errorf("expected no generated files, got: %v", res.Generated)
	}
}

// --- RenderContractArtifacts tests --------------------------------------------

// TestRenderContractArtifacts_HTTP verifies that RenderContractArtifacts
// returns 3 artifacts for an HTTP contract.
func TestRenderContractArtifacts_HTTP(t *testing.T) {
	t.Parallel()
	root, p := setupHTTPMinimalRoot(t)

	artifacts, err := RenderContractArtifacts(root, p, "http.order.ping.v1")
	if err != nil {
		t.Fatalf("RenderContractArtifacts: %v", err)
	}
	if len(artifacts) != 3 {
		t.Errorf("expected 3 artifacts for HTTP contract, got %d", len(artifacts))
	}
	fileNames := make(map[string]bool)
	for _, a := range artifacts {
		fileNames[filepath.Base(a.Path)] = true
		if len(a.Content) == 0 {
			t.Errorf("artifact %s has empty content", a.Path)
		}
	}
	for _, want := range []string{"types_gen.go", "iface_gen.go", "handler_gen.go"} {
		if !fileNames[want] {
			t.Errorf("missing artifact: %s", want)
		}
	}
}

// TestRenderContractArtifacts_Event verifies that RenderContractArtifacts
// returns 2 artifacts for an event contract (no handler_gen.go).
func TestRenderContractArtifacts_Event(t *testing.T) {
	t.Parallel()
	root, p := setupEventRoot(t)

	artifacts, err := RenderContractArtifacts(root, p, "event.item-created.v1")
	if err != nil {
		t.Fatalf("RenderContractArtifacts: %v", err)
	}
	if len(artifacts) != 2 {
		t.Errorf("expected 2 artifacts for event contract, got %d", len(artifacts))
	}
	for _, a := range artifacts {
		if filepath.Base(a.Path) == "handler_gen.go" {
			t.Error("event contract should not produce handler_gen.go")
		}
	}
}

// TestRenderContractArtifacts_CodegenFalse verifies that contracts with
// Codegen=false return (nil, nil).
func TestRenderContractArtifacts_CodegenFalse(t *testing.T) {
	t.Parallel()
	p := &metadata.ProjectMeta{
		Contracts: map[string]*metadata.ContractMeta{
			"http.foo.bar.v1": {
				ID:      "http.foo.bar.v1",
				Kind:    "http",
				Codegen: false,
			},
		},
	}
	artifacts, err := RenderContractArtifacts(t.TempDir(), p, "http.foo.bar.v1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if artifacts != nil {
		t.Errorf("expected nil artifacts for Codegen=false, got %v", artifacts)
	}
}

// TestRenderContractArtifacts_NotFound verifies error when contract id missing.
func TestRenderContractArtifacts_NotFound(t *testing.T) {
	t.Parallel()
	p := &metadata.ProjectMeta{Contracts: map[string]*metadata.ContractMeta{}}
	_, err := RenderContractArtifacts(t.TempDir(), p, "http.ghost.v1")
	if err == nil {
		t.Fatal("expected error for non-existent contract")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found', got: %v", err)
	}
}

// TestRenderContractArtifacts_NilProject verifies error for nil project.
func TestRenderContractArtifacts_NilProject(t *testing.T) {
	t.Parallel()
	_, err := RenderContractArtifacts(t.TempDir(), nil, "http.foo.v1")
	if err == nil {
		t.Fatal("expected error for nil project")
	}
}

// TestRenderContractArtifacts_RelativePaths verifies that artifact paths are
// relative (not absolute).
func TestRenderContractArtifacts_RelativePaths(t *testing.T) {
	t.Parallel()
	root, p := setupHTTPMinimalRoot(t)

	artifacts, err := RenderContractArtifacts(root, p, "http.order.ping.v1")
	if err != nil {
		t.Fatalf("RenderContractArtifacts: %v", err)
	}
	for _, a := range artifacts {
		if filepath.IsAbs(a.Path) {
			t.Errorf("artifact path should be relative, got absolute: %s", a.Path)
		}
	}
}

// TestGenerate_DryRun_DoesNotCreateDirectory verifies that DryRun does not
// create the generated/ directory tree.
func TestGenerate_DryRun_DoesNotCreateDirectory(t *testing.T) {
	t.Parallel()
	root, p := setupHTTPMinimalRoot(t)

	mustGenerate(t, root, p, Options{DryRun: true})

	genDir := filepath.Join(root, "generated")
	if _, err := os.Stat(genDir); !os.IsNotExist(err) {
		t.Errorf("DryRun should not create generated/ directory, but found it at %s", genDir)
	}
}

// --- A.8: generator tests for synth_http_full and synth_unsupported_oneof ---

// setupHTTPFullRoot copies the synth_http_full fixture into a fresh t.TempDir()
// and parses it. Returns (root, project).
func setupHTTPFullRoot(t *testing.T) (string, *metadata.ProjectMeta) {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("testdata", "synth", "synth_http_full"))
	if err != nil {
		t.Fatalf("abs path synth_http_full: %v", err)
	}
	root := t.TempDir()
	copyDirIntoTemp(t, abs, root)
	goMod := "module github.com/ghbvf/gocell\n\ngo 1.22\n"
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	p, err := metadata.NewParser(root).Parse()
	if err != nil {
		t.Fatalf("parse synth_http_full from tmp: %v", err)
	}
	return root, p
}

// setupOneOfRoot copies the synth_unsupported_oneof fixture into a fresh t.TempDir()
// and parses it. Returns (root, project).
func setupOneOfRoot(t *testing.T) (string, *metadata.ProjectMeta) {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("testdata", "synth", "synth_unsupported_oneof"))
	if err != nil {
		t.Fatalf("abs path synth_unsupported_oneof: %v", err)
	}
	root := t.TempDir()
	copyDirIntoTemp(t, abs, root)
	goMod := "module github.com/ghbvf/gocell\n\ngo 1.22\n"
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	p, err := metadata.NewParser(root).Parse()
	if err != nil {
		t.Fatalf("parse synth_unsupported_oneof from tmp: %v", err)
	}
	return root, p
}

// TestGenerate_WriteMode_HTTPFull verifies that the full HTTP fixture with path+query params
// produces 3 generated files.
func TestGenerate_WriteMode_HTTPFull(t *testing.T) {
	t.Parallel()
	root, p := setupHTTPFullRoot(t)

	res := mustGenerate(t, root, p, Options{})

	// HTTP contract → 3 files: types_gen.go, iface_gen.go, handler_gen.go.
	if len(res.Generated) != 3 {
		t.Errorf("expected 3 generated files for HTTPFull, got %d: %v", len(res.Generated), res.Generated)
	}
	for _, path := range res.Generated {
		content, err := os.ReadFile(path) //nolint:gosec // test reads its own tmp file
		if err != nil {
			t.Errorf("ReadFile %s: %v", path, err)
			continue
		}
		if len(content) == 0 {
			t.Errorf("generated file is empty: %s", path)
		}
		// Verify package name is "details" (from http.item.details.v1)
		if strings.Contains(string(content), "package details") {
			continue // correct
		}
		if strings.Contains(string(content), "package ") {
			// Wrong package name
			for _, line := range strings.Split(string(content), "\n") {
				if strings.HasPrefix(line, "package ") && !strings.Contains(line, "details") {
					t.Errorf("generated file %s has wrong package: %q", path, line)
				}
			}
		}
	}
}

// TestGenerate_BuildSpecError_OneOf verifies that a contract whose request schema
// contains an unsupported "oneOf" keyword causes Generate to return an error.
func TestGenerate_BuildSpecError_OneOf(t *testing.T) {
	t.Parallel()
	root, p := setupOneOfRoot(t)

	_, err := Generate(root, p, Options{})
	if err == nil {
		t.Fatal("expected error for contract with oneOf in request schema")
	}
	if !strings.Contains(err.Error(), "oneOf") {
		t.Errorf("error should mention 'oneOf', got: %v", err)
	}
}
