package contractgen

// command_render_test.go — golden render tests for kind=command (Batch B, #1044).
// Tests the command.tmpl template output for the synth_command fixture.
// Run with -update to regenerate golden files after intentional template changes.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/codegen"
)

// renderCommand renders command_gen.go for a spec via command.tmpl.
func renderCommand(spec *ContractGenSpec) ([]byte, error) {
	if spec.Kind != "command" {
		return nil, nil
	}
	b, err := codegen.Render("github.com/ghbvf/gocell", codegen.RenderOptions{
		TemplateName: "command.tmpl",
		Templates:    templates,
		Data:         spec,
		Filename:     "/dev/null",
	})
	if err != nil {
		return b, err
	}
	return b, nil
}

// TestRender_Golden_Synth_Command tests the command synth fixture.
// It expects types_gen.go (Request+Response DTOs) and command_gen.go
// (DispatchID + Handler + Register + Dispatch). iface_gen.go is NOT expected
// (command skips iface; Handler lives in command_gen.go).
func TestRender_Golden_Synth_Command(t *testing.T) {
	testDir := filepath.Join("testdata", "synth", "synth_command")
	absTestDir, err := filepath.Abs(testDir)
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}

	parser := metadata.NewParser(absTestDir)
	p, err := parser.Parse()
	if err != nil {
		t.Fatalf("parse synth_command fixture: %v", err)
	}
	contract := p.Contracts["command.synth.do.v1"]
	if contract == nil {
		t.Fatal("command.synth.do.v1 not found in synth fixture")
	}

	spec, err := buildContractSpec(absTestDir, p, "command.synth.do.v1")
	if err != nil {
		t.Fatalf("buildContractSpec: %v", err)
	}

	// Verify command_gen.go is expected instead of iface_gen.go.
	outputs := []string{"types_gen.go", "command_gen.go"}
	for _, outFile := range outputs {
		outFile := outFile
		t.Run(outFile, func(t *testing.T) {
			var content []byte
			switch outFile {
			case "types_gen.go":
				content, err = renderTypes(spec)
			case "command_gen.go":
				content, err = renderCommand(spec)
			}
			if err != nil {
				t.Fatalf("render %s: %v", outFile, err)
			}

			goldenFile := commandGoldenFilePath("synth_command", outFile)

			if *updateGolden {
				writeCommandGolden(t, goldenFile, content)
				return
			}
			assertCommandGolden(t, goldenFile, content)
		})
	}
}

// TestRender_Command_ContainsKeySymbols verifies that the rendered command_gen.go
// contains the expected structural elements without relying on exact golden bytes.
// This is the RED companion: ensures the template emits DispatchID, Handler,
// Register, and Dispatch before golden bytes are locked.
func TestRender_Command_ContainsKeySymbols(t *testing.T) {
	testDir := filepath.Join("testdata", "synth", "synth_command")
	absTestDir, err := filepath.Abs(testDir)
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}

	parser := metadata.NewParser(absTestDir)
	p, err := parser.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	spec, err := buildContractSpec(absTestDir, p, "command.synth.do.v1")
	if err != nil {
		t.Fatalf("buildContractSpec: %v", err)
	}

	content, err := renderCommand(spec)
	if err != nil {
		t.Fatalf("renderCommand: %v", err)
	}

	src := string(content)
	checks := []struct {
		name string
		want string
	}{
		{"DispatchID const", `const DispatchID idutil.SafeID = "command.synth.do.v1"`},
		{"Handler interface", "type Handler interface"},
		{"HandleDo method", "HandleDo(ctx context.Context, req *Request) (*Response, error)"},
		{"Register func", "func Register(reg *command.Registry, h Handler) error"},
		{"Dispatch func", "func Dispatch(ctx context.Context, reg *command.Registry, req *Request) (*Response, error)"},
		{"KindInvalid", "errcode.KindInvalid"},
		{"KindNotFound", "errcode.KindNotFound"},
		{"KindInternal", "errcode.KindInternal"},
		{"ErrCommandNotFound", "errcode.ErrCommandNotFound"},
		{"validation.IsNilInterface", "validation.IsNilInterface"},
	}

	for _, c := range checks {
		if !strings.Contains(src, c.want) {
			t.Errorf("rendered command_gen.go missing %s:\n  want substring: %q\n  (first 500 chars): %q",
				c.name, c.want, truncate(src, 500))
		}
	}

	// Verify iface Service interface is NOT in command_gen.go.
	if strings.Contains(src, "type Service interface") {
		t.Error("rendered command_gen.go must not contain 'type Service interface' (iface is skipped for command kind)")
	}
}

// TestRender_Command_IFace_Skipped verifies that generateOneContract does not
// emit iface_gen.go for kind=command (Handler lives in command_gen.go instead).
func TestRender_Command_IFace_Skipped(t *testing.T) {
	testDir := filepath.Join("testdata", "synth", "synth_command")
	absTestDir, err := filepath.Abs(testDir)
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}

	parser := metadata.NewParser(absTestDir)
	p, err := parser.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	artifacts, err := RenderContractArtifacts(absTestDir, p, "command.synth.do.v1",
		"github.com/ghbvf/gocell")
	if err != nil {
		t.Fatalf("RenderContractArtifacts: %v", err)
	}

	var paths []string
	for _, a := range artifacts {
		paths = append(paths, a.Path)
	}

	// iface_gen.go must NOT be emitted.
	for _, p := range paths {
		if strings.HasSuffix(p, "iface_gen.go") {
			t.Errorf("iface_gen.go must not be emitted for kind=command; got paths: %v", paths)
		}
	}

	// command_gen.go and types_gen.go MUST be emitted.
	hasCommandGen := false
	hasTypesGen := false
	for _, p := range paths {
		if strings.HasSuffix(p, "command_gen.go") {
			hasCommandGen = true
		}
		if strings.HasSuffix(p, "types_gen.go") {
			hasTypesGen = true
		}
	}
	if !hasCommandGen {
		t.Errorf("command_gen.go must be emitted for kind=command; got paths: %v", paths)
	}
	if !hasTypesGen {
		t.Errorf("types_gen.go must be emitted for kind=command; got paths: %v", paths)
	}
}

// commandGoldenFilePath returns the golden file path for command-specific goldens.
func commandGoldenFilePath(contractKey, outFile string) string {
	safeKey := strings.ReplaceAll(contractKey, ".", "_")
	safeKey = strings.ReplaceAll(safeKey, "-", "_")
	name := safeKey + "_" + strings.ReplaceAll(outFile, ".", "_")
	return filepath.Join(goldenDir, name+".golden")
}

// writeCommandGolden writes golden file content.
func writeCommandGolden(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write golden %s: %v", path, err)
	}
	t.Logf("updated golden file: %s", path)
}

// assertCommandGolden asserts the content matches the golden file.
func assertCommandGolden(t *testing.T, path string, got []byte) {
	t.Helper()
	want, err := os.ReadFile(path) // #nosec G304 — path is test-internal, not user input
	if err != nil {
		if os.IsNotExist(err) {
			t.Fatalf("golden file %s does not exist; run with -update to create it", path)
		}
		t.Fatalf("read golden %s: %v", path, err)
	}
	if bytes.Equal(got, want) {
		return
	}
	t.Errorf("golden mismatch %s; re-run with -update to refresh after intentional template changes", path)
}

// truncate returns the first n chars of s.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
