package contractgen

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// update flag: run with -update to regenerate golden files.
var updateGolden = flag.Bool("update", false, "update golden files")

// repoRoot returns the absolute path to the worktree root.
func repoRoot(t *testing.T) string {
	t.Helper()
	// Navigate up from testdata to find the repo root (go.mod).
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	// Walk up until we find go.mod.
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod walking up from cwd")
		}
		dir = parent
	}
}

// goldenDir is the path to the golden files relative to the package.
const goldenDir = "testdata/golden"

// TestBuildContractSpec_HTTP_OrderCreate tests BuildContractSpec for the
// todoorder ordercreate HTTP contract (POST, body, no path/query params).
func TestBuildContractSpec_HTTP_OrderCreate(t *testing.T) {
	root := repoRoot(t)
	p := loadTodoorderProject(t, root)
	// Set codegen=true for this contract.
	p.Contracts["http.order.create.v1"].Codegen = true

	spec, err := BuildContractSpec(root, p, "http.order.create.v1")
	if err != nil {
		t.Fatalf("BuildContractSpec: %v", err)
	}
	if spec.Kind != "http" {
		t.Errorf("Kind = %q, want http", spec.Kind)
	}
	if spec.PackageName != "create" {
		t.Errorf("PackageName = %q, want create", spec.PackageName)
	}
	if spec.Endpoint == nil {
		t.Fatal("Endpoint is nil")
	}
	if spec.Endpoint.Method != "POST" {
		t.Errorf("Method = %q, want POST", spec.Endpoint.Method)
	}
	if !spec.Endpoint.HasBody {
		t.Error("HasBody should be true for POST")
	}
	if spec.Endpoint.HandlerMethod != "Create" {
		t.Errorf("HandlerMethod = %q, want Create", spec.Endpoint.HandlerMethod)
	}
	if len(spec.Endpoint.PathParams) != 0 {
		t.Errorf("PathParams should be empty, got %v", spec.Endpoint.PathParams)
	}
	if len(spec.Endpoint.QueryParams) != 0 {
		t.Errorf("QueryParams should be empty, got %v", spec.Endpoint.QueryParams)
	}
	// DTOs: Request + Response + ResponseData (nested).
	if len(spec.DTOs) < 2 {
		t.Errorf("expected at least 2 DTOs, got %d: %v", len(spec.DTOs), dtoNames(spec.DTOs))
	}
}

// TestBuildContractSpec_HTTP_OrderGet tests GET with path param.
func TestBuildContractSpec_HTTP_OrderGet(t *testing.T) {
	root := repoRoot(t)
	p := loadTodoorderProject(t, root)
	p.Contracts["http.order.get.v1"].Codegen = true

	spec, err := BuildContractSpec(root, p, "http.order.get.v1")
	if err != nil {
		t.Fatalf("BuildContractSpec: %v", err)
	}
	if spec.Endpoint.Method != "GET" {
		t.Errorf("Method = %q, want GET", spec.Endpoint.Method)
	}
	if spec.Endpoint.HasBody {
		t.Error("HasBody should be false for GET")
	}
	if spec.Endpoint.HandlerMethod != "Get" {
		t.Errorf("HandlerMethod = %q, want Get", spec.Endpoint.HandlerMethod)
	}
	if len(spec.Endpoint.PathParams) != 1 {
		t.Fatalf("expected 1 path param, got %d", len(spec.Endpoint.PathParams))
	}
	if spec.Endpoint.PathParams[0].Name != "id" {
		t.Errorf("PathParams[0].Name = %q, want id", spec.Endpoint.PathParams[0].Name)
	}
	// Request DTO should have ID field from path param (initialism: id → ID).
	reqDTO := findDTO(spec.DTOs, "Request")
	if reqDTO == nil {
		t.Fatal("Request DTO not found")
	}
	if findField(reqDTO, "ID") == nil {
		t.Error("Request DTO should have ID field from path param")
	}
}

// TestBuildContractSpec_HTTP_OrderList tests GET with query params.
func TestBuildContractSpec_HTTP_OrderList(t *testing.T) {
	root := repoRoot(t)
	p := loadTodoorderProject(t, root)
	p.Contracts["http.order.list.v1"].Codegen = true

	spec, err := BuildContractSpec(root, p, "http.order.list.v1")
	if err != nil {
		t.Fatalf("BuildContractSpec: %v", err)
	}
	if spec.Endpoint.HandlerMethod != "List" {
		t.Errorf("HandlerMethod = %q, want List", spec.Endpoint.HandlerMethod)
	}
	if len(spec.Endpoint.QueryParams) != 2 {
		t.Fatalf("expected 2 query params, got %d: %v", len(spec.Endpoint.QueryParams), spec.Endpoint.QueryParams)
	}
	// cursor (string) and limit (integer) — sorted alphabetically.
	cursorIdx, limitIdx := -1, -1
	for i, q := range spec.Endpoint.QueryParams {
		switch q.Name {
		case "cursor":
			cursorIdx = i
		case "limit":
			limitIdx = i
		}
	}
	if cursorIdx == -1 {
		t.Error("cursor query param not found")
	}
	if limitIdx == -1 {
		t.Error("limit query param not found")
	}
	if cursorIdx != -1 && spec.Endpoint.QueryParams[cursorIdx].GoType != "string" {
		t.Errorf("cursor GoType = %q, want string", spec.Endpoint.QueryParams[cursorIdx].GoType)
	}
	if limitIdx != -1 && spec.Endpoint.QueryParams[limitIdx].GoType != "int64" {
		t.Errorf("limit GoType = %q, want int64", spec.Endpoint.QueryParams[limitIdx].GoType)
	}
}

// TestBuildContractSpec_Event_OrderCreated tests event contract.
func TestBuildContractSpec_Event_OrderCreated(t *testing.T) {
	root := repoRoot(t)
	p := loadTodoorderProject(t, root)
	p.Contracts["event.order-created.v1"].Codegen = true

	spec, err := BuildContractSpec(root, p, "event.order-created.v1")
	if err != nil {
		t.Fatalf("BuildContractSpec: %v", err)
	}
	if spec.Kind != "event" {
		t.Errorf("Kind = %q, want event", spec.Kind)
	}
	if spec.Endpoint != nil {
		t.Error("Endpoint should be nil for event")
	}
	if spec.Event == nil {
		t.Fatal("Event is nil")
	}
	if spec.Event.HandlerMethod != "HandleOrderCreated" {
		t.Errorf("HandlerMethod = %q, want HandleOrderCreated", spec.Event.HandlerMethod)
	}
	if spec.Event.Topic != "event.order-created.v1" {
		t.Errorf("Topic = %q, want event.order-created.v1", spec.Event.Topic)
	}
	if !spec.Event.Replayable {
		t.Error("Replayable should be true")
	}
	// Should have Payload DTO + Headers DTO.
	if findDTO(spec.DTOs, "Payload") == nil {
		t.Error("Payload DTO not found")
	}
	if findDTO(spec.DTOs, "Headers") == nil {
		t.Error("Headers DTO not found")
	}
}

// TestBuildContractSpec_ContractNotFound tests error on missing contract.
func TestBuildContractSpec_ContractNotFound(t *testing.T) {
	root := repoRoot(t)
	p := loadTodoorderProject(t, root)
	_, err := BuildContractSpec(root, p, "http.does.not.exist.v1")
	if err == nil {
		t.Fatal("expected error for missing contract")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found', got: %v", err)
	}
}

// TestBuildContractSpec_CodegenFalse tests error when Codegen=false.
func TestBuildContractSpec_CodegenFalse(t *testing.T) {
	// Use a synthetic project with codegen explicitly false.
	p := &metadata.ProjectMeta{
		Contracts: map[string]*metadata.ContractMeta{
			"http.synth.nocodegen.v1": {
				ID:      "http.synth.nocodegen.v1",
				Kind:    "http",
				Codegen: false,
			},
		},
	}
	root := findRepoRoot()
	_, err := BuildContractSpec(root, p, "http.synth.nocodegen.v1")
	if err == nil {
		t.Fatal("expected error for codegen=false")
	}
	if !strings.Contains(err.Error(), "codegen=false") {
		t.Errorf("error should mention 'codegen=false', got: %v", err)
	}
}

// TestBuildContractSpec_MissingHTTPEndpoint tests error when http endpoint is missing.
func TestBuildContractSpec_MissingHTTPEndpoint(t *testing.T) {
	p := &metadata.ProjectMeta{
		Contracts: map[string]*metadata.ContractMeta{
			"http.foo.bar.v1": {
				ID:      "http.foo.bar.v1",
				Kind:    "http",
				Codegen: true,
				// No HTTP endpoint.
			},
		},
	}
	root := findRepoRoot()
	_, err := BuildContractSpec(root, p, "http.foo.bar.v1")
	if err == nil {
		t.Fatal("expected error for missing http endpoint")
	}
}

// TestBuildContractSpec_MissingPayloadRef tests error when event has no payload.
func TestBuildContractSpec_MissingPayloadRef(t *testing.T) {
	p := &metadata.ProjectMeta{
		Contracts: map[string]*metadata.ContractMeta{
			"event.foo.bar.v1": {
				ID:      "event.foo.bar.v1",
				Kind:    "event",
				Codegen: true,
				// No schemaRefs.
			},
		},
	}
	root := findRepoRoot()
	_, err := BuildContractSpec(root, p, "event.foo.bar.v1")
	if err == nil {
		t.Fatal("expected error for missing payload schemaRef")
	}
}

// TestBuildContractSpec_CommandKind_GracefulSkip verifies that kind=command is
// accepted without error. command and projection are in the closed set but do not
// yet have full generators — only types_gen.go + iface_gen.go are emitted.
func TestBuildContractSpec_CommandKind_GracefulSkip(t *testing.T) {
	p := &metadata.ProjectMeta{
		Contracts: map[string]*metadata.ContractMeta{
			"command.foo.bar.v1": {
				ID:      "command.foo.bar.v1",
				Kind:    "command",
				Codegen: true,
			},
		},
	}
	root := findRepoRoot()
	spec, err := BuildContractSpec(root, p, "command.foo.bar.v1")
	if err != nil {
		t.Fatalf("BuildContractSpec should not error for kind=command (graceful skip), got: %v", err)
	}
	if spec == nil || spec.Kind != "command" {
		t.Errorf("expected spec with Kind=command, got: %v", spec)
	}
	// No Endpoint or Event fields populated for command kind.
	if spec.Endpoint != nil {
		t.Errorf("spec.Endpoint should be nil for kind=command, got non-nil")
	}
	if spec.Event != nil {
		t.Errorf("spec.Event should be nil for kind=command, got non-nil")
	}
}

// TestBuildContractSpec_TrulyUnsupportedKind verifies that a kind not in the
// closed set (http | event | command | projection) returns an error.
func TestBuildContractSpec_TrulyUnsupportedKind(t *testing.T) {
	p := &metadata.ProjectMeta{
		Contracts: map[string]*metadata.ContractMeta{
			"workflow.foo.bar.v1": {
				ID:      "workflow.foo.bar.v1",
				Kind:    "workflow",
				Codegen: true,
			},
		},
	}
	root := findRepoRoot()
	_, err := BuildContractSpec(root, p, "workflow.foo.bar.v1")
	if err == nil {
		t.Fatal("expected error for truly unsupported kind")
	}
}

// --- Golden file tests ---

// TestRender_Golden runs BuildContractSpec + render for the 4 real todoorder
// contracts and compares the output to golden files.
// Run with -update to regenerate golden files.
func TestRender_Golden(t *testing.T) {
	root := repoRoot(t)
	p := loadTodoorderProject(t, root)

	cases := []struct {
		contractID string
		kind       string
		outputs    []string
	}{
		{"http.order.create.v1", "http", []string{"types_gen.go", "iface_gen.go", "handler_gen.go"}},
		{"http.order.get.v1", "http", []string{"types_gen.go", "iface_gen.go", "handler_gen.go"}},
		{"http.order.list.v1", "http", []string{"types_gen.go", "iface_gen.go", "handler_gen.go"}},
		{"event.order-created.v1", "event", []string{"types_gen.go", "iface_gen.go", "spec_gen.go", "subscription_gen.go"}},
	}

	for _, tc := range cases {
		t.Run(tc.contractID, func(t *testing.T) {
			// Enable codegen for this contract.
			contract := p.Contracts[tc.contractID]
			if contract == nil {
				t.Fatalf("contract %q not found in project", tc.contractID)
			}
			contract.Codegen = true
			defer func() { contract.Codegen = false }()

			spec, err := BuildContractSpec(root, p, tc.contractID)
			if err != nil {
				t.Fatalf("BuildContractSpec(%q): %v", tc.contractID, err)
			}

			for _, outFile := range tc.outputs {
				t.Run(outFile, func(t *testing.T) {
					content := renderFile(t, spec, outFile)
					goldenFile := goldenFilePath(tc.contractID, outFile)

					if *updateGolden {
						writeGolden(t, goldenFile, content)
						return
					}
					assertGolden(t, goldenFile, content)
				})
			}
		})
	}
}

// TestRender_Golden_Synth_HTTPMinimal tests the minimal HTTP synth fixture.
func TestRender_Golden_Synth_HTTPMinimal(t *testing.T) {
	testDir := filepath.Join("testdata", "synth", "synth_http_minimal")
	absTestDir, err := filepath.Abs(testDir)
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}

	parser := metadata.NewParser(absTestDir)
	p, err := parser.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	contract := p.Contracts["http.order.ping.v1"]
	if contract == nil {
		t.Fatal("http.order.ping.v1 not found in synth fixture")
	}

	outputs := []string{"types_gen.go", "iface_gen.go", "handler_gen.go"}
	for _, outFile := range outputs {
		t.Run(outFile, func(t *testing.T) {
			spec, err := BuildContractSpec(absTestDir, p, "http.order.ping.v1")
			if err != nil {
				t.Fatalf("BuildContractSpec: %v", err)
			}
			content := renderFile(t, spec, outFile)
			goldenFile := goldenFilePath("synth_http_minimal", outFile)

			if *updateGolden {
				writeGolden(t, goldenFile, content)
				return
			}
			assertGolden(t, goldenFile, content)
		})
	}
}

// TestRender_Golden_Synth_HTTPFull tests the full HTTP synth fixture with path+query params.
func TestRender_Golden_Synth_HTTPFull(t *testing.T) {
	testDir := filepath.Join("testdata", "synth", "synth_http_full")
	absTestDir, err := filepath.Abs(testDir)
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}

	parser := metadata.NewParser(absTestDir)
	p, err := parser.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	contract := p.Contracts["http.item.details.v1"]
	if contract == nil {
		t.Fatal("http.item.details.v1 not found in synth fixture")
	}

	outputs := []string{"types_gen.go", "iface_gen.go", "handler_gen.go"}
	for _, outFile := range outputs {
		t.Run(outFile, func(t *testing.T) {
			spec, err := BuildContractSpec(absTestDir, p, "http.item.details.v1")
			if err != nil {
				t.Fatalf("BuildContractSpec: %v", err)
			}
			content := renderFile(t, spec, outFile)
			goldenFile := goldenFilePath("synth_http_full", outFile)

			if *updateGolden {
				writeGolden(t, goldenFile, content)
				return
			}
			assertGolden(t, goldenFile, content)
		})
	}
}

// TestRender_Golden_Synth_Event tests the event synth fixture.
func TestRender_Golden_Synth_Event(t *testing.T) {
	testDir := filepath.Join("testdata", "synth", "synth_event")
	absTestDir, err := filepath.Abs(testDir)
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}

	parser := metadata.NewParser(absTestDir)
	p, err := parser.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	contract := p.Contracts["event.item-created.v1"]
	if contract == nil {
		t.Fatal("event.item-created.v1 not found in synth fixture")
	}

	outputs := []string{"types_gen.go", "iface_gen.go", "spec_gen.go", "subscription_gen.go"}
	for _, outFile := range outputs {
		t.Run(outFile, func(t *testing.T) {
			spec, err := BuildContractSpec(absTestDir, p, "event.item-created.v1")
			if err != nil {
				t.Fatalf("BuildContractSpec: %v", err)
			}
			content := renderFile(t, spec, outFile)
			goldenFile := goldenFilePath("synth_event", outFile)

			if *updateGolden {
				writeGolden(t, goldenFile, content)
				return
			}
			assertGolden(t, goldenFile, content)
		})
	}
}

// --- helpers ---

// loadTodoorderProject parses the todoorder example project metadata.
func loadTodoorderProject(t *testing.T, root string) *metadata.ProjectMeta {
	t.Helper()
	parser := metadata.NewParser(root)
	p, err := parser.Parse()
	if err != nil {
		t.Fatalf("parse project: %v", err)
	}
	return p
}

// findRepoRoot walks up from cwd to find the go.mod root.
func findRepoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		panic(fmt.Sprintf("getwd: %v", err))
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			panic("could not find go.mod walking up from cwd")
		}
		dir = parent
	}
}

// renderFile invokes the appropriate render function based on the file name.
func renderFile(t *testing.T, spec *ContractGenSpec, outFile string) []byte {
	t.Helper()
	var (
		content []byte
		err     error
	)
	switch outFile {
	case "types_gen.go":
		content, err = RenderTypes(spec, "/dev/null")
	case "iface_gen.go":
		content, err = RenderIface(spec, "/dev/null")
	case "handler_gen.go":
		content, err = RenderHandler(spec, "/dev/null")
	case "spec_gen.go":
		content, err = RenderSpec(spec, "/dev/null")
	case "subscription_gen.go":
		content, err = RenderSubscription(spec, "/dev/null")
	default:
		t.Fatalf("unknown output file: %s", outFile)
	}
	if err != nil {
		t.Fatalf("render %s: %v", outFile, err)
	}
	return content
}

// goldenFilePath returns the path to the golden file for a given contract and output file.
func goldenFilePath(contractKey, outFile string) string {
	safeKey := strings.ReplaceAll(contractKey, ".", "_")
	safeKey = strings.ReplaceAll(safeKey, "-", "_")
	name := safeKey + "_" + strings.ReplaceAll(outFile, ".", "_")
	return filepath.Join(goldenDir, name+".golden")
}

func writeGolden(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write golden %s: %v", path, err)
	}
	t.Logf("updated golden file: %s", path)
}

func assertGolden(t *testing.T, path string, got []byte) {
	t.Helper()
	want, err := os.ReadFile(path) // #nosec G304 — path is test-internal, not user input
	if err != nil {
		if os.IsNotExist(err) {
			t.Fatalf("golden file %s does not exist; run with -update to create it", path)
		}
		t.Fatalf("read golden %s: %v", path, err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("output does not match golden file %s\n\ngot:\n%s\n\nwant:\n%s", path, got, want)
	}
}

// TestRender_Golden_Synth_KeywordConflict tests that a contract whose action segment
// collides with a Go keyword (delete) generates package name "configdelete".
func TestRender_Golden_Synth_KeywordConflict(t *testing.T) {
	testDir := filepath.Join("testdata", "synth", "synth_http_keyword_conflict")
	absTestDir, err := filepath.Abs(testDir)
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}

	parser := metadata.NewParser(absTestDir)
	p, err := parser.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	contract := p.Contracts["http.config.delete.v1"]
	if contract == nil {
		t.Fatal("http.config.delete.v1 not found in synth fixture")
	}

	outputs := []string{"types_gen.go", "iface_gen.go", "handler_gen.go"}
	for _, outFile := range outputs {
		t.Run(outFile, func(t *testing.T) {
			spec, err := BuildContractSpec(absTestDir, p, "http.config.delete.v1")
			if err != nil {
				t.Fatalf("BuildContractSpec: %v", err)
			}
			// Verify keyword sanitization.
			if spec.PackageName != "configdelete" {
				t.Errorf("PackageName = %q, want configdelete", spec.PackageName)
			}
			content := renderFile(t, spec, outFile)
			goldenFile := goldenFilePath("synth_http_keyword_conflict", outFile)

			if *updateGolden {
				writeGolden(t, goldenFile, content)
				return
			}
			assertGolden(t, goldenFile, content)
		})
	}
}

// --- A.9: BuildContractSpec integration tests using synth fixtures ---

// TestBuildContractSpec_HTTP uses the synth_http_full fixture to verify
// Endpoint.Method, SuccessCode, path params, and DTOs.
func TestBuildContractSpec_HTTP(t *testing.T) {
	testDir := filepath.Join("testdata", "synth", "synth_http_full")
	absTestDir, err := filepath.Abs(testDir)
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}
	parser := metadata.NewParser(absTestDir)
	p, err := parser.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	spec, err := BuildContractSpec(absTestDir, p, "http.item.details.v1")
	if err != nil {
		t.Fatalf("BuildContractSpec: %v", err)
	}
	if spec.Endpoint == nil {
		t.Fatal("Endpoint is nil")
	}
	if spec.Endpoint.Method != "GET" {
		t.Errorf("Method = %q, want GET", spec.Endpoint.Method)
	}
	if spec.Endpoint.SuccessCode != 200 {
		t.Errorf("SuccessCode = %d, want 200", spec.Endpoint.SuccessCode)
	}
	if len(spec.Endpoint.PathParams) != 1 {
		t.Fatalf("expected 1 path param, got %d", len(spec.Endpoint.PathParams))
	}
	if spec.Endpoint.PathParams[0].Name != "id" {
		t.Errorf("PathParams[0].Name = %q, want id", spec.Endpoint.PathParams[0].Name)
	}
	if spec.Endpoint.PathParams[0].GoName != "ID" {
		t.Errorf("PathParams[0].GoName = %q, want ID", spec.Endpoint.PathParams[0].GoName)
	}
	if spec.PackageName != "details" {
		t.Errorf("PackageName = %q, want details", spec.PackageName)
	}
	if len(spec.DTOs) == 0 {
		t.Error("expected non-empty DTOs")
	}
}

// TestBuildContractSpec_Event uses the synth_event fixture to verify
// Event.Topic, HandlerMethod, and Payload DTO presence.
func TestBuildContractSpec_Event(t *testing.T) {
	testDir := filepath.Join("testdata", "synth", "synth_event")
	absTestDir, err := filepath.Abs(testDir)
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}
	parser := metadata.NewParser(absTestDir)
	p, err := parser.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	spec, err := BuildContractSpec(absTestDir, p, "event.item-created.v1")
	if err != nil {
		t.Fatalf("BuildContractSpec: %v", err)
	}
	if spec.Event == nil {
		t.Fatal("Event is nil")
	}
	if spec.Event.Topic != "event.item-created.v1" {
		t.Errorf("Topic = %q, want event.item-created.v1", spec.Event.Topic)
	}
	if spec.PackageName != "itemcreated" {
		t.Errorf("PackageName = %q, want itemcreated", spec.PackageName)
	}
	if findDTO(spec.DTOs, "Payload") == nil {
		t.Error("Payload DTO not found")
	}
}

// findDTO finds a DTOSpec by name in a slice.
func findDTO(dtos []DTOSpec, name string) *DTOSpec {
	for i := range dtos {
		if dtos[i].Name == name {
			return &dtos[i]
		}
	}
	return nil
}

// findField finds a DTOField by name in a DTOSpec.
func findField(dto *DTOSpec, name string) *DTOField {
	for i := range dto.Fields {
		if dto.Fields[i].Name == name {
			return &dto.Fields[i]
		}
	}
	return nil
}

// --- TDD tests for spec_gen.go / subscription_gen.go ---

// TestSpecGenIsPackagePrivate verifies that the generated spec var is lowercase (private).
func TestSpecGenIsPackagePrivate(t *testing.T) {
	t.Parallel()
	testDir := filepath.Join("testdata", "synth", "synth_event")
	absTestDir, err := filepath.Abs(testDir)
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}
	parser := metadata.NewParser(absTestDir)
	p, err := parser.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	spec, err := BuildContractSpec(absTestDir, p, "event.item-created.v1")
	if err != nil {
		t.Fatalf("BuildContractSpec: %v", err)
	}

	content, err := RenderSpec(spec, "/dev/null")
	if err != nil {
		t.Fatalf("RenderSpec: %v", err)
	}
	got := string(content)

	if !strings.Contains(got, "var spec = wrapper.ContractSpec{") {
		t.Errorf("expected private var spec, not found in:\n%s", got)
	}
	if strings.Contains(got, "var Spec") {
		t.Errorf("spec var must not be exported (Spec), found in:\n%s", got)
	}
}

// TestSubscriptionMountCallsRegistrySubscribe verifies the generated Mount method
// calls reg.Subscribe with the correct arguments including WithSubscriptionSliceID.
func TestSubscriptionMountCallsRegistrySubscribe(t *testing.T) {
	t.Parallel()
	testDir := filepath.Join("testdata", "synth", "synth_event")
	absTestDir, err := filepath.Abs(testDir)
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}
	parser := metadata.NewParser(absTestDir)
	p, err := parser.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	spec, err := BuildContractSpec(absTestDir, p, "event.item-created.v1")
	if err != nil {
		t.Fatalf("BuildContractSpec: %v", err)
	}

	content, err := RenderSubscription(spec, "/dev/null")
	if err != nil {
		t.Fatalf("RenderSubscription: %v", err)
	}
	got := string(content)

	if !strings.Contains(got, "func (s *Subscription) Mount(reg cell.Registry) error {") {
		t.Errorf("expected Mount method signature, not found in:\n%s", got)
	}
	if !strings.Contains(got, "reg.Subscribe(spec, s.handler, s.consumerGroup,") {
		t.Errorf("expected reg.Subscribe call, not found in:\n%s", got)
	}
	if !strings.Contains(got, "cell.WithSubscriptionSliceID(s.sliceID)") {
		t.Errorf("expected WithSubscriptionSliceID call, not found in:\n%s", got)
	}
}

// TestNewSubscriptionFourArgSignature verifies the constructor signature.
func TestNewSubscriptionFourArgSignature(t *testing.T) {
	t.Parallel()
	testDir := filepath.Join("testdata", "synth", "synth_event")
	absTestDir, err := filepath.Abs(testDir)
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}
	parser := metadata.NewParser(absTestDir)
	p, err := parser.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	spec, err := BuildContractSpec(absTestDir, p, "event.item-created.v1")
	if err != nil {
		t.Fatalf("BuildContractSpec: %v", err)
	}

	content, err := RenderSubscription(spec, "/dev/null")
	if err != nil {
		t.Fatalf("RenderSubscription: %v", err)
	}
	got := string(content)

	if !strings.Contains(got, "func NewSubscription(handler outbox.EntryHandler, consumerGroup, sliceID string) *Subscription {") {
		t.Errorf("expected 4-arg NewSubscription signature, not found in:\n%s", got)
	}
	// No fluent options.
	if strings.Contains(got, "WithSliceID") {
		t.Errorf("unexpected WithSliceID method (fluent option), found in:\n%s", got)
	}
}

// TestRenderSpec_RejectsHTTPContract verifies that RenderSpec returns an error
// when the contract is kind=http.
func TestRenderSpec_RejectsHTTPContract(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	p := loadTodoorderProject(t, root)
	p.Contracts["http.order.create.v1"].Codegen = true

	spec, err := BuildContractSpec(root, p, "http.order.create.v1")
	if err != nil {
		t.Fatalf("BuildContractSpec: %v", err)
	}

	_, err = RenderSpec(spec, "/dev/null")
	if err == nil {
		t.Fatal("RenderSpec should reject kind=http contract")
	}
	if !strings.Contains(err.Error(), "not event") {
		t.Errorf("error should mention 'not event', got: %v", err)
	}

	_, err = RenderSubscription(spec, "/dev/null")
	if err == nil {
		t.Fatal("RenderSubscription should reject kind=http contract")
	}
	if !strings.Contains(err.Error(), "not event") {
		t.Errorf("error should mention 'not event', got: %v", err)
	}
}

// TestGenerateEventContract_EmitsSpecAndSubscription verifies that Generate for
// an event contract produces spec_gen.go and subscription_gen.go in addition
// to types_gen.go and iface_gen.go.
func TestGenerateEventContract_EmitsSpecAndSubscription(t *testing.T) {
	t.Parallel()
	root, p := setupEventRoot(t)

	res := mustGenerate(t, root, p, Options{})

	fileNames := make(map[string]bool)
	for _, path := range res.Generated {
		fileNames[filepath.Base(path)] = true
	}

	if !fileNames["spec_gen.go"] {
		t.Errorf("spec_gen.go not generated; got: %v", res.Generated)
	}
	if !fileNames["subscription_gen.go"] {
		t.Errorf("subscription_gen.go not generated; got: %v", res.Generated)
	}

	// Verify content of spec_gen.go is non-empty and has expected markers.
	for _, path := range res.Generated {
		if filepath.Base(path) == "spec_gen.go" {
			content, err := os.ReadFile(path) //nolint:gosec // test reads its own tmp file
			if err != nil {
				t.Fatalf("read spec_gen.go: %v", err)
			}
			if !strings.Contains(string(content), "var spec = wrapper.ContractSpec{") {
				t.Errorf("spec_gen.go missing private spec var:\n%s", content)
			}
		}
		if filepath.Base(path) == "subscription_gen.go" {
			content, err := os.ReadFile(path) //nolint:gosec // test reads its own tmp file
			if err != nil {
				t.Fatalf("read subscription_gen.go: %v", err)
			}
			if !strings.Contains(string(content), "func NewSubscription(") {
				t.Errorf("subscription_gen.go missing NewSubscription:\n%s", content)
			}
			if !strings.Contains(string(content), "func (s *Subscription) Mount(reg cell.Registry) error") {
				t.Errorf("subscription_gen.go missing Mount:\n%s", content)
			}
		}
	}
}
