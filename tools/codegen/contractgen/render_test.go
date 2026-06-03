package contractgen

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/metadata/metadatatest"
	"github.com/ghbvf/gocell/pkg/testutil/fileutil"
	"github.com/ghbvf/gocell/tools/codegen"
)

// renderTypes / renderIface / renderHandler / renderSpec / renderSubscription
// are test-only wrappers over codegen.Render for one contractgen artifact.
// Production rendering goes through Generate / RenderContractArtifacts
// (generator.go), which call codegen.Render directly; these helpers let the
// per-template unit + golden tests render a single artifact in isolation and
// exercise the kind-gating guards. Filename is fixed to "/dev/null" (rendering
// to memory — goimports path-aware import resolution disabled), matching the
// committed goldens.
func renderTypes(spec *ContractGenSpec) ([]byte, error) {
	b, err := codegen.Render("github.com/ghbvf/gocell", codegen.RenderOptions{
		TemplateName: "types.tmpl", Templates: templates, Data: spec, Filename: "/dev/null",
	})
	if err != nil {
		return b, fmt.Errorf("contractgen render types: %w", err)
	}
	return b, nil
}

func renderIface(spec *ContractGenSpec) ([]byte, error) {
	b, err := codegen.Render("github.com/ghbvf/gocell", codegen.RenderOptions{
		TemplateName: "iface.tmpl", Templates: templates, Data: spec, Filename: "/dev/null",
	})
	if err != nil {
		return b, fmt.Errorf("contractgen render iface: %w", err)
	}
	return b, nil
}

func renderHandler(spec *ContractGenSpec) ([]byte, error) {
	if spec.Kind != "http" {
		return nil, fmt.Errorf("contractgen render handler: contract %q is kind=%q, not http", spec.ContractID, spec.Kind)
	}
	b, err := codegen.Render("github.com/ghbvf/gocell", codegen.RenderOptions{
		TemplateName: "handler.tmpl", Templates: templates, Data: spec, Filename: "/dev/null",
	})
	if err != nil {
		return b, fmt.Errorf("contractgen render handler: %w", err)
	}
	return b, nil
}

func renderSpec(spec *ContractGenSpec) ([]byte, error) {
	if spec.Kind != "event" {
		return nil, fmt.Errorf("contractgen render spec: contract %q is kind=%q, not event", spec.ContractID, spec.Kind)
	}
	b, err := codegen.Render("github.com/ghbvf/gocell", codegen.RenderOptions{
		TemplateName: "spec.tmpl", Templates: templates, Data: spec, Filename: "/dev/null",
	})
	if err != nil {
		return b, fmt.Errorf("contractgen render spec: %w", err)
	}
	return b, nil
}

func renderSubscription(spec *ContractGenSpec) ([]byte, error) {
	if spec.Kind != "event" {
		return nil, fmt.Errorf("contractgen render subscription: contract %q is kind=%q, not event", spec.ContractID, spec.Kind)
	}
	b, err := codegen.Render("github.com/ghbvf/gocell", codegen.RenderOptions{
		TemplateName: "subscription.tmpl", Templates: templates, Data: spec, Filename: "/dev/null",
	})
	if err != nil {
		return b, fmt.Errorf("contractgen render subscription: %w", err)
	}
	return b, nil
}

func renderSaga(spec *ContractGenSpec) ([]byte, error) {
	if spec.Kind != "saga" {
		return nil, fmt.Errorf("contractgen render saga: contract %q is kind=%q, not saga", spec.ContractID, spec.Kind)
	}
	b, err := codegen.Render("github.com/ghbvf/gocell", codegen.RenderOptions{
		TemplateName: "saga.tmpl", Templates: templates, Data: spec, Filename: "/dev/null",
	})
	if err != nil {
		return b, fmt.Errorf("contractgen render saga: %w", err)
	}
	return b, nil
}

func renderProjection(spec *ContractGenSpec) ([]byte, error) {
	if spec.Kind != "event" {
		return nil, fmt.Errorf("contractgen render projection: contract %q is kind=%q, not event", spec.ContractID, spec.Kind)
	}
	b, err := codegen.Render("github.com/ghbvf/gocell", codegen.RenderOptions{
		TemplateName: "projection.tmpl", Templates: templates, Data: spec, Filename: "/dev/null",
	})
	if err != nil {
		return b, fmt.Errorf("contractgen render projection: %w", err)
	}
	return b, nil
}

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

// TestBuildContractSpec_HTTP_OrderCreate tests buildContractSpec for the
// todoorder ordercreate HTTP contract (POST, body, no path/query params).
func TestBuildContractSpec_HTTP_OrderCreate(t *testing.T) {
	root := repoRoot(t)
	p := loadTodoorderProject(t, root)
	// Set codegen=true for this contract.
	p.Contracts["http.order.create.v1"].Codegen = true

	spec, err := buildContractSpec(root, p, "http.order.create.v1")
	if err != nil {
		t.Fatalf("buildContractSpec: %v", err)
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

	spec, err := buildContractSpec(root, p, "http.order.get.v1")
	if err != nil {
		t.Fatalf("buildContractSpec: %v", err)
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

	spec, err := buildContractSpec(root, p, "http.order.list.v1")
	if err != nil {
		t.Fatalf("buildContractSpec: %v", err)
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

	spec, err := buildContractSpec(root, p, "event.order-created.v1")
	if err != nil {
		t.Fatalf("buildContractSpec: %v", err)
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

// TestRender_Event_Transports verifies the spec.tmpl transport derivation (#1389):
// the generated ContractSpec.Transport is always the primary (Transports[0]), and
// the Transports() copy-returning accessor (+ its unexported backing slice) is
// emitted ONLY for multi-transport contracts. This locks the
// {{if gt (len .Transports) 1}} branch in contractgen's own tests (the real
// device-registered regen is the integration witness; this is the unit).
//
// Derivation lock (NOT a hardcoded primary): both cases drive a NON-amqp primary
// (mqtt) precisely so the assertions distinguish `index .Transports 0` derivation
// from a hardcoded `Transport: "amqp"` literal. A template regression to a fixed
// transport string fails here even though the device-registered golden (amqp
// primary) would not catch it — the golden locks regen consistency, this locks
// that the primary tracks transports[0]. See the #1389 ADR for why this unit
// derivation lock supersedes a redundant TRANSPORT-SEALED-FUNNEL archtest (the
// transport→ContractSpec surface is already sealed by codegen-golden +
// NO-MANUAL-CONTRACTSPEC-LITERAL-01).
func TestRender_Event_Transports(t *testing.T) {
	root := repoRoot(t)
	renderSpec := func(t *testing.T, transports []string) string {
		t.Helper()
		p := loadTodoorderProject(t, root)
		c := p.Contracts["event.order-created.v1"]
		c.Codegen = true
		c.Transports = transports
		spec, err := buildContractSpec(root, p, "event.order-created.v1")
		if err != nil {
			t.Fatalf("buildContractSpec: %v", err)
		}
		out, err := codegen.Render("github.com/ghbvf/gocell", codegen.RenderOptions{
			TemplateName: "spec.tmpl", Templates: templates, Data: spec, Filename: "/dev/null",
		})
		if err != nil {
			t.Fatalf("Render spec.tmpl: %v", err)
		}
		return string(out)
	}

	t.Run("single transport: primary derives transports[0], no Transports accessor", func(t *testing.T) {
		out := renderSpec(t, []string{"mqtt"})
		if !strings.Contains(out, `Transport: "mqtt"`) {
			t.Errorf("expected primary Transport \"mqtt\" (transports[0]), got:\n%s", out)
		}
		if strings.Contains(out, `Transport: "amqp"`) {
			t.Errorf("primary must derive transports[0]=mqtt, not a hardcoded \"amqp\", got:\n%s", out)
		}
		if strings.Contains(out, "Transports") {
			t.Errorf("single-transport contract must NOT emit a Transports accessor/var, got:\n%s", out)
		}
	})

	t.Run("multi transport: primary is transports[0] + copy-returning accessor in declared order", func(t *testing.T) {
		// mqtt FIRST so the primary is provably transports[0], not a hardcoded amqp.
		out := renderSpec(t, []string{"mqtt", "amqp"})
		if !strings.Contains(out, `Transport: "mqtt"`) {
			t.Errorf("expected primary Transport \"mqtt\" (transports[0]), got:\n%s", out)
		}
		// Unexported backing slice (declared order) — the only mutable holder.
		if !strings.Contains(out, `var transports = []string{"mqtt", "amqp"}`) {
			t.Errorf("expected unexported backing slice in declared order, got:\n%s", out)
		}
		// Exported accessor returns a fresh copy so importers cannot mutate truth source.
		if !strings.Contains(out, `func Transports() []string`) {
			t.Errorf("expected exported Transports() accessor, got:\n%s", out)
		}
		if !strings.Contains(out, `return append([]string(nil), transports...)`) {
			t.Errorf("Transports() must return a copy (append([]string(nil), …)), got:\n%s", out)
		}
		// No exported MUTABLE var (the holder must be the unexported backing slice).
		if strings.Contains(out, `var Transports =`) {
			t.Errorf("must NOT expose a mutable exported var Transports, got:\n%s", out)
		}
	})
}

// TestRender_HTTP_Transport locks the handler.tmpl transport derivation (#1389):
// the generated contractSpec.Transport must derive from Transports[0], not be
// a hardcoded "http" literal in the template.
//
// Derivation lock (NOT a hardcoded primary): the HTTP spec is built with a
// NON-"http" primary (mqtt) by directly overwriting spec.Transports after
// buildContractSpec. This bypasses governance FMT-39, which is fine here — the
// point is a pure render derivation lock. A regression that hardcodes
// Transport: "http" in handler.tmpl would produce Transport: "http" even though
// Transports[0]="mqtt", causing the assertion below to fail.
//
// Mutation check: if handler.tmpl said `Transport: "http"` hardcoded, the
// rendered output would contain `Transport: "http"` not `Transport: "mqtt"`,
// and strings.Contains(out, `Transport: "mqtt"`) would be false — test fails.
func TestRender_HTTP_Transport(t *testing.T) {
	root := repoRoot(t)
	p := loadTodoorderProject(t, root)
	p.Contracts["http.order.create.v1"].Codegen = true

	spec, err := buildContractSpec(root, p, "http.order.create.v1")
	if err != nil {
		t.Fatalf("buildContractSpec: %v", err)
	}

	// Inject a non-"http" primary transport directly on the spec to prove the
	// template derives Transport from Transports[0], not from a hardcoded "http".
	spec.Transports = []string{"mqtt"}

	out, err := codegen.Render("github.com/ghbvf/gocell", codegen.RenderOptions{
		TemplateName: "handler.tmpl", Templates: templates, Data: spec, Filename: "/dev/null",
	})
	if err != nil {
		t.Fatalf("Render handler.tmpl: %v", err)
	}
	got := string(out)

	if !strings.Contains(got, `Transport: "mqtt"`) {
		t.Errorf("expected primary Transport \"mqtt\" (transports[0]) in contractSpec block, got:\n%s", got)
	}
	if strings.Contains(got, `Transport: "http"`) {
		t.Errorf("Transport must derive transports[0]=mqtt, not a hardcoded \"http\", got:\n%s", got)
	}
}

// TestRender_ExternalModulePath proves modulePath flows through contractgen
// rendering: rendering the subscription template (which imports framework
// kernel packages) under an EXTERNAL module path produces valid Go, and the
// framework kernel import is grouped as third-party (#1083). In an external
// repo, github.com/ghbvf/gocell/kernel/... is a go-get dependency, not local.
func TestRender_ExternalModulePath(t *testing.T) {
	root := repoRoot(t)
	p := loadTodoorderProject(t, root)
	p.Contracts["event.order-created.v1"].Codegen = true
	spec, err := buildContractSpec(root, p, "event.order-created.v1")
	if err != nil {
		t.Fatalf("buildContractSpec: %v", err)
	}

	const extMod = "github.com/acme/svc"
	out, err := codegen.Render(extMod, codegen.RenderOptions{
		TemplateName: "subscription.tmpl", Templates: templates, Data: spec, Filename: "/dev/null",
	})
	if err != nil {
		t.Fatalf("Render with external module path: %v", err)
	}
	// The framework kernel import must still be present (it is a real dependency
	// of the generated code) and must NOT be pinned into a local block keyed to
	// the external module — i.e. rendering did not hardcode github.com/ghbvf/gocell
	// as the local prefix.
	if !bytes.Contains(out, []byte("github.com/ghbvf/gocell/kernel/")) {
		t.Errorf("expected framework kernel import in rendered subscription, got:\n%s", out)
	}
	if bytes.Contains(out, []byte(extMod)) {
		t.Errorf("did not expect external module %q to appear in subscription output:\n%s", extMod, out)
	}
}

// TestBuildContractSpec_ContractNotFound tests error on missing contract.
func TestBuildContractSpec_ContractNotFound(t *testing.T) {
	root := repoRoot(t)
	p := loadTodoorderProject(t, root)
	_, err := buildContractSpec(root, p, "http.does.not.exist.v1")
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
	_, err := buildContractSpec(root, p, "http.synth.nocodegen.v1")
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
				ID:         "http.foo.bar.v1",
				Kind:       "http",
				Codegen:    true,
				Transports: []string{"http"}, // mirrors parser defaultTransportsForKind("http")
				// No HTTP endpoint.
			},
		},
	}
	root := findRepoRoot()
	_, err := buildContractSpec(root, p, "http.foo.bar.v1")
	if err == nil {
		t.Fatal("expected error for missing http endpoint")
	}
}

// TestBuildContractSpec_MissingPayloadRef tests error when event has no payload.
func TestBuildContractSpec_MissingPayloadRef(t *testing.T) {
	p := &metadata.ProjectMeta{
		Contracts: map[string]*metadata.ContractMeta{
			"event.foo.bar.v1": {
				ID:         "event.foo.bar.v1",
				Kind:       "event",
				Codegen:    true,
				Transports: []string{"amqp"}, // mirrors parser defaultTransportsForKind("event")
				// No schemaRefs.
			},
		},
	}
	root := findRepoRoot()
	_, err := buildContractSpec(root, p, "event.foo.bar.v1")
	if err == nil {
		t.Fatal("expected error for missing payload schemaRef")
	}
}

// TestBuildContractSpec_EmptyTransports verifies that buildContractSpec returns
// a clear error when contract.Transports is nil/empty rather than panicking
// inside a template at `index .Transports 0`. This covers the codegen path that
// does NOT run governance FMT-39 (e.g. direct generate invocation).
func TestBuildContractSpec_EmptyTransports(t *testing.T) {
	p := &metadata.ProjectMeta{
		Contracts: map[string]*metadata.ContractMeta{
			"http.synth.emptytransports.v1": {
				ID:         "http.synth.emptytransports.v1",
				Kind:       "http",
				Codegen:    true,
				Transports: nil, // simulate unknown kind / missing parser default
			},
		},
	}
	root := findRepoRoot()
	_, err := buildContractSpec(root, p, "http.synth.emptytransports.v1")
	if err == nil {
		t.Fatal("expected error for empty transports, got nil")
	}
	if !strings.Contains(err.Error(), "empty transports") {
		t.Errorf("error should mention 'empty transports', got: %v", err)
	}
	if !strings.Contains(err.Error(), "FMT-39") {
		t.Errorf("error should reference governance rule FMT-39, got: %v", err)
	}
}

// TestBuildContractSpec_CommandKind_GracefulSkip verifies that kind=command is
// accepted without error. command and projection are in the closed set but do not
// yet have full generators — only types_gen.go + iface_gen.go are emitted.
func TestBuildContractSpec_CommandKind_GracefulSkip(t *testing.T) {
	p := &metadata.ProjectMeta{
		Contracts: map[string]*metadata.ContractMeta{
			"command.foo.bar.v1": {
				ID:         "command.foo.bar.v1",
				Kind:       "command",
				Codegen:    true,
				Transports: []string{"amqp"}, // mirrors parser defaultTransportsForKind("command")
			},
		},
	}
	root := findRepoRoot()
	spec, err := buildContractSpec(root, p, "command.foo.bar.v1")
	if err != nil {
		t.Fatalf("buildContractSpec should not error for kind=command (graceful skip), got: %v", err)
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
// closed set (http | event | command | projection | webhook | grpc | saga) returns an error.
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
	_, err := buildContractSpec(root, p, "workflow.foo.bar.v1")
	if err == nil {
		t.Fatal("expected error for truly unsupported kind")
	}
}

// --- Golden file tests ---

// TestRender_Golden runs buildContractSpec + render for the 4 real todoorder
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
		{"event.order-created.v1", "event", []string{"types_gen.go", "iface_gen.go", "spec_gen.go", "subscription_gen.go", "projection_gen.go"}},
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

			spec, err := buildContractSpec(root, p, tc.contractID)
			if err != nil {
				t.Fatalf("buildContractSpec(%q): %v", tc.contractID, err)
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
			spec, err := buildContractSpec(absTestDir, p, "http.order.ping.v1")
			if err != nil {
				t.Fatalf("buildContractSpec: %v", err)
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
			spec, err := buildContractSpec(absTestDir, p, "http.item.details.v1")
			if err != nil {
				t.Fatalf("buildContractSpec: %v", err)
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

// TestRender_Golden_Synth_HTTPAuthModes tests the auth-modes synth fixture
// (Public + Bootstrap branches of handler.tmpl). Pinning these golden bytes
// prevents silent removal of branch-specific literals: Public NewHandler with
// no policy arg, Bootstrap nil-bootstrapAuth panic, and the schema-compile
// panic on each branch.
func TestRender_Golden_Synth_HTTPAuthModes(t *testing.T) {
	testDir := filepath.Join("testdata", "synth", "synth_http_auth_modes")
	absTestDir, err := filepath.Abs(testDir)
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}

	parser := metadata.NewParser(absTestDir)
	p, err := parser.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// To extend coverage with a new auth mode (e.g. a future auth.serviceToken):
	//   1. Add testdata/synth/synth_http_auth_modes/contracts/http/sample/<mode>/v1/
	//      with contract.yaml (declaring the new auth flag) + request.schema.json
	//      + response.schema.json.
	//   2. Append a case below with contractID and goldenKey.
	//   3. Run: go test ./tools/codegen/contractgen/ \
	//      -run TestRender_Golden_Synth_HTTPAuthModes -update
	cases := []struct {
		contractID string
		goldenKey  string
	}{
		{"http.sample.public.v1", "synth_http_auth_modes_public"},
		{"http.sample.bootstrap.v1", "synth_http_auth_modes_bootstrap"},
		{"http.sample.passwordresetexempt.v1", "synth_http_auth_modes_passwordresetexempt"},
		{"http.sample.clientsonly.v1", "synth_http_auth_modes_clientsonly"},
		{"http.sample.serviceowned.v1", "synth_http_auth_modes_serviceowned"},
	}

	outputs := []string{"types_gen.go", "iface_gen.go", "handler_gen.go"}
	for _, tc := range cases {
		t.Run(tc.contractID, func(t *testing.T) {
			contract := p.Contracts[tc.contractID]
			if contract == nil {
				t.Fatalf("%s not found in synth fixture", tc.contractID)
			}
			spec, err := buildContractSpec(absTestDir, p, tc.contractID)
			if err != nil {
				t.Fatalf("buildContractSpec: %v", err)
			}
			for _, outFile := range outputs {
				t.Run(outFile, func(t *testing.T) {
					content := renderFile(t, spec, outFile)
					goldenFile := goldenFilePath(tc.goldenKey, outFile)

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

	outputs := []string{"types_gen.go", "iface_gen.go", "spec_gen.go", "subscription_gen.go", "projection_gen.go"}
	for _, outFile := range outputs {
		t.Run(outFile, func(t *testing.T) {
			spec, err := buildContractSpec(absTestDir, p, "event.item-created.v1")
			if err != nil {
				t.Fatalf("buildContractSpec: %v", err)
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

func TestRender_Golden_Synth_Saga(t *testing.T) {
	testDir := filepath.Join("testdata", "synth", "synth_saga")
	absTestDir, err := filepath.Abs(testDir)
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}

	parser := metadata.NewParser(absTestDir)
	p, err := parser.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	contract := p.Contracts["saga.orderfulfillment.v1"]
	if contract == nil {
		t.Fatal("saga.orderfulfillment.v1 not found in synth fixture")
	}

	// types_gen.go = step output DTOs; iface_gen.go = empty (no http Service);
	// saga_gen.go = DefinitionID + Impl + BuildDefinition + Register.
	outputs := []string{"types_gen.go", "iface_gen.go", "saga_gen.go"}
	for _, outFile := range outputs {
		t.Run(outFile, func(t *testing.T) {
			spec, err := buildContractSpec(absTestDir, p, "saga.orderfulfillment.v1")
			if err != nil {
				t.Fatalf("buildContractSpec: %v", err)
			}
			content := renderFile(t, spec, outFile)
			goldenFile := goldenFilePath("synth_saga", outFile)

			if *updateGolden {
				writeGolden(t, goldenFile, content)
				return
			}
			assertGolden(t, goldenFile, content)
		})
	}
}

// TestRender_Golden_Synth_GRPC tests the grpc placeholder-stub fixture. A
// kind=grpc contract emits the universal types_gen.go (near-empty: no JSON-schema
// DTOs — proto is the schema, deferred to PR 6) + iface_gen.go (the []byte
// placeholder Server interface). No handler/spec/subscription artifacts.
func TestRender_Golden_Synth_GRPC(t *testing.T) {
	testDir := filepath.Join("testdata", "synth", "synth_grpc_minimal")
	absTestDir, err := filepath.Abs(testDir)
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}

	parser := metadata.NewParser(absTestDir)
	p, err := parser.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	contract := p.Contracts["grpc.device.command.v1"]
	if contract == nil {
		t.Fatal("grpc.device.command.v1 not found in synth fixture")
	}

	outputs := []string{"types_gen.go", "iface_gen.go"}
	for _, outFile := range outputs {
		t.Run(outFile, func(t *testing.T) {
			spec, err := buildContractSpec(absTestDir, p, "grpc.device.command.v1")
			if err != nil {
				t.Fatalf("buildContractSpec: %v", err)
			}
			content := renderFile(t, spec, outFile)
			goldenFile := goldenFilePath("synth_grpc_minimal", outFile)

			if *updateGolden {
				writeGolden(t, goldenFile, content)
				return
			}
			assertGolden(t, goldenFile, content)
		})
	}
}

// TestBuildContractSpec_Saga asserts the saga IR: step output DTOs, the
// chained input types (step N input = step N-1 output; step 0 has none), and
// per-step compensate derivation (createShipment opts out with compensate:false).
func TestBuildContractSpec_Saga(t *testing.T) {
	testDir := filepath.Join("testdata", "synth", "synth_saga")
	absTestDir, err := filepath.Abs(testDir)
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}
	parser := metadata.NewParser(absTestDir)
	p, err := parser.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	spec, err := buildContractSpec(absTestDir, p, "saga.orderfulfillment.v1")
	if err != nil {
		t.Fatalf("buildContractSpec: %v", err)
	}
	if spec.Saga == nil {
		t.Fatal("spec.Saga is nil for kind=saga")
	}
	if got, want := len(spec.Saga.Steps), 3; got != want {
		t.Fatalf("steps: got %d want %d", got, want)
	}

	steps := spec.Saga.Steps
	// Step 0 takes no typed input; later steps chain the prior output type.
	if !steps[0].IsFirst || steps[0].InputGoType != "" {
		t.Errorf("step0: IsFirst=%v InputGoType=%q; want first with empty input", steps[0].IsFirst, steps[0].InputGoType)
	}
	if steps[0].OutputGoType != "ReserveInventoryOutput" {
		t.Errorf("step0 output: got %q", steps[0].OutputGoType)
	}
	if steps[1].InputGoType != "ReserveInventoryOutput" {
		t.Errorf("step1 input: got %q want ReserveInventoryOutput", steps[1].InputGoType)
	}
	if steps[2].InputGoType != "ChargePaymentOutput" {
		t.Errorf("step2 input: got %q want ChargePaymentOutput", steps[2].InputGoType)
	}
	// compensate defaults true; createShipment opts out.
	if !steps[0].HasCompensate || !steps[1].HasCompensate {
		t.Errorf("steps 0,1 should compensate: %v %v", steps[0].HasCompensate, steps[1].HasCompensate)
	}
	if steps[2].HasCompensate {
		t.Error("step2 (createShipment) declared compensate:false but HasCompensate=true")
	}
	// Output DTOs are emitted for every step (rendered by types.tmpl).
	for _, want := range []string{"ReserveInventoryOutput", "ChargePaymentOutput", "CreateShipmentOutput"} {
		if !hasDTONamed(spec.DTOs, want) {
			t.Errorf("missing output DTO %q in spec.DTOs", want)
		}
	}
	// Durations parsed into readable Go exprs.
	if spec.Saga.TimeoutExpr != "30 * time.Second" {
		t.Errorf("saga timeout expr: got %q want %q", spec.Saga.TimeoutExpr, "30 * time.Second")
	}
	if !spec.Saga.NeedsTime {
		t.Error("NeedsTime should be true (durations present)")
	}
}

// TestBuildSagaSpec_GoNameCollisionRejected asserts that two steps whose names
// collapse to the same PascalCase Go identifier (e.g. "reserve" / "Reserve")
// are rejected at buildSagaSpec time with a clear error message naming both
// steps and the colliding identifier.
func TestBuildSagaSpec_GoNameCollisionRejected(t *testing.T) {
	root, contractDir := synthSagaContractDir(t)
	c := &metadata.ContractMeta{
		ID: "saga.orderfulfillment.v1", Kind: "saga",
		File: contractDir + "/contract.yaml",
		Saga: &metadata.SagaMeta{
			Steps: []metadata.SagaStepMeta{
				{Name: "reserve", Output: "reserve-inventory.output.schema.json"},
				{Name: "Reserve", Output: "charge-payment.output.schema.json"},
			},
		},
	}
	spec := &ContractGenSpec{ContractID: c.ID, Kind: c.Kind}
	err := buildSagaSpec(spec, root, c, contractDir)
	if err == nil {
		t.Fatal("expected collision error for steps 'reserve'/'Reserve', got nil")
	}
	if !strings.Contains(err.Error(), "Reserve") {
		t.Errorf("error should mention the colliding identifier, got: %v", err)
	}
	if !strings.Contains(err.Error(), "rename one step") {
		t.Errorf("error should suggest renaming a step, got: %v", err)
	}
}

// TestBuildSagaSpec_RetryPolicyMaxIntervalBelowBaseRejected asserts that a
// retries block where maxInterval < baseInterval is rejected at buildSagaSpec.
func TestBuildSagaSpec_RetryPolicyMaxIntervalBelowBaseRejected(t *testing.T) {
	root, contractDir := synthSagaContractDir(t)
	c := &metadata.ContractMeta{
		ID: "saga.orderfulfillment.v1", Kind: "saga",
		File: contractDir + "/contract.yaml",
		Saga: &metadata.SagaMeta{
			Retries: &metadata.SagaRetryMeta{
				MaxAttempts:  3,
				BaseInterval: "30s",
				MaxInterval:  "1s",
			},
			Steps: []metadata.SagaStepMeta{
				{Name: "reserveInventory", Output: "reserve-inventory.output.schema.json"},
			},
		},
	}
	spec := &ContractGenSpec{ContractID: c.ID, Kind: c.Kind}
	err := buildSagaSpec(spec, root, c, contractDir)
	if err == nil {
		t.Fatal("expected error for maxInterval < baseInterval, got nil")
	}
	if !strings.Contains(err.Error(), "MaxInterval") {
		t.Errorf("error should mention MaxInterval, got: %v", err)
	}
}

// TestBuildSagaSpec_CompensationOrderRejected asserts a non-reverse
// compensationOrder fails the build (the only supported value is "reverse").
func TestBuildSagaSpec_CompensationOrderRejected(t *testing.T) {
	c := &metadata.ContractMeta{
		ID:   "saga.x.v1",
		Kind: "saga",
		File: "contracts/saga/x/v1/contract.yaml",
		Saga: &metadata.SagaMeta{
			CompensationOrder: "forward",
			Steps:             []metadata.SagaStepMeta{{Name: "a", Output: "a.json"}},
		},
	}
	spec := &ContractGenSpec{ContractID: c.ID, Kind: c.Kind}
	err := buildSagaSpec(spec, ".", c, "contracts/saga/x/v1")
	if err == nil {
		t.Fatal("expected error for compensationOrder=forward, got nil")
	}
	if !strings.Contains(err.Error(), "compensationOrder") {
		t.Errorf("error should mention compensationOrder: %v", err)
	}
}

// synthSagaContractDir returns (rootDir, contractDir) for the synth_saga
// fixture so buildSagaSpec can resolve the committed output schema files.
func synthSagaContractDir(t *testing.T) (string, string) {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("testdata", "synth", "synth_saga"))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	return root, "contracts/saga/orderfulfillment/v1"
}

// TestBuildContractSpec_SagaSingleStep covers the step-0-only path: a one-step
// saga whose single step takes no typed input and (default) compensates.
func TestBuildContractSpec_SagaSingleStep(t *testing.T) {
	root, contractDir := synthSagaContractDir(t)
	c := &metadata.ContractMeta{
		ID: "saga.orderfulfillment.v1", Kind: "saga",
		File: contractDir + "/contract.yaml",
		Saga: &metadata.SagaMeta{
			Steps: []metadata.SagaStepMeta{
				{Name: "reserveInventory", Output: "reserve-inventory.output.schema.json"},
			},
		},
	}
	spec := &ContractGenSpec{ContractID: c.ID, Kind: c.Kind, PackageName: "orderfulfillment"}
	if err := buildSagaSpec(spec, root, c, contractDir); err != nil {
		t.Fatalf("buildSagaSpec: %v", err)
	}
	if len(spec.Saga.Steps) != 1 {
		t.Fatalf("steps = %d, want 1", len(spec.Saga.Steps))
	}
	s0 := spec.Saga.Steps[0]
	if !s0.IsFirst || s0.InputGoType != "" {
		t.Errorf("single step must be first with empty input: IsFirst=%v input=%q", s0.IsFirst, s0.InputGoType)
	}
	if !s0.HasCompensate {
		t.Error("step with no compensate field should default to HasCompensate=true")
	}
	// Render saga_gen.go and assert the Impl interface has Run but no other Run methods.
	out := string(renderFile(t, spec, "saga_gen.go"))
	if !strings.Contains(out, "RunReserveInventory(ctx context.Context, inst *saga.Instance) (ReserveInventoryOutput, error)") {
		t.Errorf("single-step Impl Run signature missing/incorrect:\n%s", out)
	}
}

// TestBuildContractSpec_SagaAllCompensateFalse covers the all-no-compensation
// path: every step opts out, so the Impl exposes zero Compensate methods.
func TestBuildContractSpec_SagaAllCompensateFalse(t *testing.T) {
	root, contractDir := synthSagaContractDir(t)
	no := false
	c := &metadata.ContractMeta{
		ID: "saga.orderfulfillment.v1", Kind: "saga",
		File: contractDir + "/contract.yaml",
		Saga: &metadata.SagaMeta{
			Steps: []metadata.SagaStepMeta{
				{Name: "reserveInventory", Output: "reserve-inventory.output.schema.json", Compensate: &no},
				{Name: "chargePayment", Output: "charge-payment.output.schema.json", Compensate: &no},
				{Name: "createShipment", Output: "create-shipment.output.schema.json", Compensate: &no},
			},
		},
	}
	spec := &ContractGenSpec{ContractID: c.ID, Kind: c.Kind, PackageName: "orderfulfillment"}
	if err := buildSagaSpec(spec, root, c, contractDir); err != nil {
		t.Fatalf("buildSagaSpec: %v", err)
	}
	for i, s := range spec.Saga.Steps {
		if s.HasCompensate {
			t.Errorf("step[%d] %q: HasCompensate=true, want false", i, s.Name)
		}
	}
	out := string(renderFile(t, spec, "saga_gen.go"))
	// "Compensate" appears in the Impl godoc prose; assert the absence of the
	// actual wired field and interface method instead.
	if strings.Contains(out, "Compensate: func(") {
		t.Errorf("all-compensate-false saga must wire no saga.Step.Compensate field:\n%s", out)
	}
	if strings.Contains(out, "\tCompensateReserveInventory(") {
		t.Errorf("all-compensate-false saga must emit no Compensate Impl methods:\n%s", out)
	}
}

// TestBuildSagaSpec_RejectsTraversalOutput asserts the path-traversal guard:
// an output ref escaping the contract directory is rejected at build time.
func TestBuildSagaSpec_RejectsTraversalOutput(t *testing.T) {
	root, contractDir := synthSagaContractDir(t)
	c := &metadata.ContractMeta{
		ID: "saga.orderfulfillment.v1", Kind: "saga",
		File: contractDir + "/contract.yaml",
		Saga: &metadata.SagaMeta{
			Steps: []metadata.SagaStepMeta{
				{Name: "evil", Output: "../../../../../etc/passwd"},
			},
		},
	}
	spec := &ContractGenSpec{ContractID: c.ID, Kind: c.Kind}
	err := buildSagaSpec(spec, root, c, contractDir)
	if err == nil {
		t.Fatal("expected error for traversal output path, got nil")
	}
	if !strings.Contains(err.Error(), "contract-relative") {
		t.Errorf("error should explain the contract-relative constraint: %v", err)
	}
}

// TestRenderSaga_RejectsNonSagaContract mirrors renderSpec/renderSubscription's
// kind-gate: feeding a non-saga spec to renderSaga is a programmer error.
func TestRenderSaga_RejectsNonSagaContract(t *testing.T) {
	_, err := renderSaga(&ContractGenSpec{ContractID: "http.x.v1", Kind: "http"})
	if err == nil {
		t.Fatal("expected error rendering saga for non-saga contract")
	}
	if !strings.Contains(err.Error(), "not saga") {
		t.Errorf("error should mention 'not saga': %v", err)
	}
}

// TestSagaGen_NilImplGuards pins F7: the generated Register / BuildDefinition
// fail fast on a nil impl (typed OR untyped — validation.IsNilInterface) at
// registration time, instead of deferring the nil-deref to step execution.
// Register returns an error (error-first); BuildDefinition has no error return
// (its result feeds variadic NewInMemoryRegistry), so it fails fast via the
// panicregister funnel. This is an explicit guard against a future -update
// silently stripping the checks from the golden.
func TestSagaGen_NilImplGuards(t *testing.T) {
	root, contractDir := synthSagaContractDir(t)
	c := &metadata.ContractMeta{
		ID: "saga.orderfulfillment.v1", Kind: "saga",
		File: contractDir + "/contract.yaml",
		Saga: &metadata.SagaMeta{
			Steps: []metadata.SagaStepMeta{
				{Name: "reserveInventory", Output: "reserve-inventory.output.schema.json"},
			},
		},
	}
	spec := &ContractGenSpec{ContractID: c.ID, Kind: c.Kind, PackageName: "orderfulfillment"}
	if err := buildSagaSpec(spec, root, c, contractDir); err != nil {
		t.Fatalf("buildSagaSpec: %v", err)
	}
	out := string(renderFile(t, spec, "saga_gen.go"))
	// Both Register and BuildDefinition guard impl via the typed-nil-safe helper.
	if n := strings.Count(out, "validation.IsNilInterface(impl)"); n != 2 {
		t.Errorf("expected 2 validation.IsNilInterface(impl) guards (Register + BuildDefinition), got %d:\n%s", n, out)
	}
	// Register fails fast with an error (error-first path).
	if !strings.Contains(out, `"saga register: impl must not be nil"`) {
		t.Errorf("Register must return a nil-impl error:\n%s", out)
	}
	// BuildDefinition fails fast via the panicregister funnel (no error return).
	if !strings.Contains(out, `panicregister.Approved("saga-build-definition-nil-impl"`) {
		t.Errorf("BuildDefinition must fail-fast on nil impl via the panicregister funnel:\n%s", out)
	}
}

// TestSagaGoldenCompiles is the permanent guard the golden-diff alone cannot
// provide: it compiles the generated saga package (types_gen.go + saga_gen.go)
// against the real kernel/saga, catching type-level template breakage (wrong
// method signatures, missing imports) that a byte-diff would miss.
func TestSagaGoldenCompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping go-build compile guard in -short mode")
	}
	root := repoRoot(t) // worktree module root (has go.mod for github.com/ghbvf/gocell)
	absTestDir, contractDir := synthSagaContractDir(t)
	_ = contractDir
	parser := metadata.NewParser(absTestDir)
	p, err := parser.Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	spec, err := buildContractSpec(absTestDir, p, "saga.orderfulfillment.v1")
	if err != nil {
		t.Fatalf("buildContractSpec: %v", err)
	}

	dir := t.TempDir()
	writeFile := func(name string, content []byte) {
		// #nosec G703 G304 -- test-internal: dir is t.TempDir(), name is a literal.
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	writeFile("types_gen.go", renderFile(t, spec, "types_gen.go"))
	writeFile("saga_gen.go", renderFile(t, spec, "saga_gen.go"))
	// Self-contained module that resolves the in-repo kernel/saga via a local
	// replace. Reuse the worktree's `go` directive so the temp main module is
	// not older than the replaced dependency (else go build refuses).
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
	gomod := "module sagagoldencompile\n\n" + goDirective +
		"\n\nrequire github.com/ghbvf/gocell v0.0.0\n\nreplace github.com/ghbvf/gocell => " + root + "\n"
	writeFile("go.mod", []byte(gomod))

	cmd := exec.Command("go", "build", "./...")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated saga package failed to compile: %v\n%s", err, out)
	}
}

// TestDurationExpr covers the duration→Go-expression helper.
func TestDurationExpr(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "", false},
		{"0s", "", false},
		{"30s", "30 * time.Second", false},
		{"5m", "5 * time.Minute", false},
		{"100ms", "100 * time.Millisecond", false},
		{"2h", "2 * time.Hour", false},
		{"1500ms", "1500 * time.Millisecond", false},
		{"500us", "500 * time.Microsecond", false},
		{"750ns", "750 * time.Nanosecond", false},
		{"-1s", "", true},
		{"notaduration", "", true},
	}
	for _, tc := range cases {
		got, err := durationExpr(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("durationExpr(%q): expected error, got %q", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("durationExpr(%q): unexpected error %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("durationExpr(%q) = %q, want %q", tc.in, got, tc.want)
		}
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
		content, err = renderTypes(spec)
	case "iface_gen.go":
		content, err = renderIface(spec)
	case "handler_gen.go":
		content, err = renderHandler(spec)
	case "spec_gen.go":
		content, err = renderSpec(spec)
	case "subscription_gen.go":
		content, err = renderSubscription(spec)
	case "projection_gen.go":
		content, err = renderProjection(spec)
	case "saga_gen.go":
		content, err = renderSaga(spec)
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
	if bytes.Equal(got, want) {
		return
	}
	gotLines := strings.Split(string(got), "\n")
	wantLines := strings.Split(string(want), "\n")
	maxLines := len(gotLines)
	if len(wantLines) > maxLines {
		maxLines = len(wantLines)
	}
	for i := 0; i < maxLines; i++ {
		var gotLine, wantLine string
		if i < len(gotLines) {
			gotLine = gotLines[i]
		}
		if i < len(wantLines) {
			wantLine = wantLines[i]
		}
		if gotLine != wantLine {
			t.Errorf("golden mismatch %s line %d:\n  got:  %q\n  want: %q\n(re-run with -update to refresh after intentional template changes)",
				path, i+1, gotLine, wantLine)
			return
		}
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
			spec, err := buildContractSpec(absTestDir, p, "http.config.delete.v1")
			if err != nil {
				t.Fatalf("buildContractSpec: %v", err)
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

// --- A.9: buildContractSpec integration tests using synth fixtures ---

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

	spec, err := buildContractSpec(absTestDir, p, "http.item.details.v1")
	if err != nil {
		t.Fatalf("buildContractSpec: %v", err)
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

	spec, err := buildContractSpec(absTestDir, p, "event.item-created.v1")
	if err != nil {
		t.Fatalf("buildContractSpec: %v", err)
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

	spec, err := buildContractSpec(absTestDir, p, "event.item-created.v1")
	if err != nil {
		t.Fatalf("buildContractSpec: %v", err)
	}

	content, err := renderSpec(spec)
	if err != nil {
		t.Fatalf("renderSpec: %v", err)
	}
	got := string(content)

	if !strings.Contains(got, "var spec = contractspec.ContractSpec{") {
		t.Errorf("expected private var spec, not found in:\n%s", got)
	}
	if strings.Contains(got, "var Spec") {
		t.Errorf("spec var must not be exported (Spec), found in:\n%s", got)
	}
}

// TestSubscriptionMountCallsRegistrySubscribe verifies the generated Mount method
// calls reg.Subscribe with cellID + sliceID (post-K#07 HARD-positional cellID).
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

	spec, err := buildContractSpec(absTestDir, p, "event.item-created.v1")
	if err != nil {
		t.Fatalf("buildContractSpec: %v", err)
	}

	content, err := renderSubscription(spec)
	if err != nil {
		t.Fatalf("renderSubscription: %v", err)
	}
	got := string(content)

	if !strings.Contains(got, "func (s *Subscription) Mount(reg cell.Registrar) error {") {
		t.Errorf("expected Mount method signature, not found in:\n%s", got)
	}
	// K#07: cellID is the 4th positional parameter (HARD contract).
	if !strings.Contains(got, "reg.Subscribe(spec, s.handler, s.consumerGroup, s.cellID,") {
		t.Errorf("expected reg.Subscribe call with positional s.cellID, not found in:\n%s", got)
	}
	if !strings.Contains(got, "cell.WithSubscriptionSliceID(s.sliceID)") {
		t.Errorf("expected WithSubscriptionSliceID call, not found in:\n%s", got)
	}
}

// TestNewSubscriptionFourArgSignature verifies the constructor signature post-K#07:
// NewSubscription(handler, consumerGroup, cellID, sliceID) — four parameters,
// all mandatory positional. cellID is HARD-required to mirror the
// Registry.Subscribe contract; omitting it would be a compile failure at the
// cellgen Mount call site.
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

	spec, err := buildContractSpec(absTestDir, p, "event.item-created.v1")
	if err != nil {
		t.Fatalf("buildContractSpec: %v", err)
	}

	content, err := renderSubscription(spec)
	if err != nil {
		t.Fatalf("renderSubscription: %v", err)
	}
	got := string(content)

	if !strings.Contains(got, "func NewSubscription(handler outbox.EntryHandler, consumerGroup, cellID, sliceID string) *Subscription {") {
		t.Errorf("expected 4-arg NewSubscription signature with cellID, not found in:\n%s", got)
	}
	// No fluent options for cellID — K#07 HARD contract requires positional.
	if strings.Contains(got, "WithSubscriptionCellID") {
		t.Errorf("unexpected WithSubscriptionCellID fluent option — cellID must be positional (HARD), found in:\n%s", got)
	}
	if strings.Contains(got, "WithSliceID") {
		t.Errorf("unexpected WithSliceID method (fluent option), found in:\n%s", got)
	}
}

// TestRenderSpec_RejectsHTTPContract verifies that renderSpec returns an error
// when the contract is kind=http.
func TestRenderSpec_RejectsHTTPContract(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	p := loadTodoorderProject(t, root)
	p.Contracts["http.order.create.v1"].Codegen = true

	spec, err := buildContractSpec(root, p, "http.order.create.v1")
	if err != nil {
		t.Fatalf("buildContractSpec: %v", err)
	}

	_, err = renderSpec(spec)
	if err == nil {
		t.Fatal("renderSpec should reject kind=http contract")
	}
	if !strings.Contains(err.Error(), "not event") {
		t.Errorf("error should mention 'not event', got: %v", err)
	}

	_, err = renderSubscription(spec)
	if err == nil {
		t.Fatal("renderSubscription should reject kind=http contract")
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

	res := mustGenerate(t, root, p, Options{Scope: ScopeAll{}, ModulePath: "github.com/ghbvf/gocell"})

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
			content := fileutil.MustReadFile(t, path)
			if !strings.Contains(string(content), "var spec = contractspec.ContractSpec{") {
				t.Errorf("spec_gen.go missing private spec var:\n%s", content)
			}
		}
		if filepath.Base(path) == "subscription_gen.go" {
			content := fileutil.MustReadFile(t, path)
			if !strings.Contains(string(content), "func NewSubscription(") {
				t.Errorf("subscription_gen.go missing NewSubscription:\n%s", content)
			}
			if !strings.Contains(string(content), "func (s *Subscription) Mount(reg cell.Registrar) error") {
				t.Errorf("subscription_gen.go missing Mount:\n%s", content)
			}
		}
	}
}

// TestRenderProjection_ContractIDConsumedInGodoc asserts that the rendered
// projection_gen.go for the event.order-created.v1 contract contains the
// literal contract ID in the godoc comment. This proves the template consumes
// spec.ContractID (e.g. "consumes event.order-created.v1") rather than a
// hardcoded string, so a future contract rename will surface as a golden diff.
func TestRenderProjection_ContractIDConsumedInGodoc(t *testing.T) {
	t.Parallel()
	root := repoRoot(t)
	p := loadTodoorderProject(t, root)
	p.Contracts["event.order-created.v1"].Codegen = true

	spec, err := buildContractSpec(root, p, "event.order-created.v1")
	if err != nil {
		t.Fatalf("buildContractSpec: %v", err)
	}

	content, err := renderProjection(spec)
	if err != nil {
		t.Fatalf("renderProjection: %v", err)
	}
	got := string(content)

	const wantContractID = "event.order-created.v1"
	if !strings.Contains(got, wantContractID) {
		t.Errorf("projection_gen.go godoc must contain contract ID %q; rendered output:\n%s", wantContractID, got)
	}
}

// --- funcMap unit tests (PR #403 review T1: needsStrconv / responseGoTypeName /
// liftHTTPResponses were previously covered only via golden files; direct unit
// cases isolate logic regressions before they propagate into rendered output).

// TestNeedsStrconv covers the package-level helper that decides whether the
// generated handler imports strconv. Pagination ExtraQueryParams take
// precedence — when set, the full QueryParams slice is irrelevant.
func TestNeedsStrconv(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		spec *ContractGenSpec
		want bool
	}{
		{"nil spec", nil, false},
		{"nil endpoint", &ContractGenSpec{}, false},
		{"non-pagination string-only", &ContractGenSpec{Endpoint: &httpEndpointSpec{
			QueryParams: []ParamSpec{{Name: "name", GoType: "string"}},
		}}, false},
		{"non-pagination int64", &ContractGenSpec{Endpoint: &httpEndpointSpec{
			QueryParams: []ParamSpec{{Name: "page", GoType: "int64"}},
		}}, true},
		{"pagination no extras", &ContractGenSpec{Endpoint: &httpEndpointSpec{
			Pagination: &PaginationShape{HasCursor: true, HasLimit: true},
		}}, false},
		{"pagination with int64 extra", &ContractGenSpec{Endpoint: &httpEndpointSpec{
			Pagination: &PaginationShape{
				HasCursor: true, HasLimit: true,
				ExtraQueryParams: []ParamSpec{{Name: "since", GoType: "int64"}},
			},
		}}, true},
		{"pagination with bool extra", &ContractGenSpec{Endpoint: &httpEndpointSpec{
			Pagination: &PaginationShape{
				HasCursor: true, HasLimit: true,
				ExtraQueryParams: []ParamSpec{{Name: "active", GoType: "bool"}},
			},
		}}, true},
		{"pagination with string-only extras", &ContractGenSpec{Endpoint: &httpEndpointSpec{
			Pagination: &PaginationShape{
				HasCursor: true, HasLimit: true,
				ExtraQueryParams: []ParamSpec{{Name: "tag", GoType: "string"}},
			},
		}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := needsStrconv(tc.spec); got != tc.want {
				t.Errorf("needsStrconv(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// TestResponseGoTypeName covers the {HandlerMethod}{Status}{Suffix} naming
// convention emitted into types_gen.go. Three suffixes — JSONResponse for
// body-bearing success, NoContentResponse for 204 success markers,
// ErrorResponse for declared 4xx/5xx — are the entire surface CH-06
// validates against the contract.yaml declaration.
func TestResponseGoTypeName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		method      string
		status      int
		isNoContent bool
		want        string
	}{
		{"Get", 200, false, "Get200JSONResponse"},
		{"Create", 201, false, "Create201JSONResponse"},
		{"Delete", 204, true, "Delete204NoContentResponse"},
		{"Get", 404, false, "Get404ErrorResponse"},
		{"Get", 503, false, "Get503ErrorResponse"},
		{"HandleEnqueue", 201, false, "HandleEnqueue201JSONResponse"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			got := responseGoTypeName(tc.method, tc.status, tc.isNoContent)
			if got != tc.want {
				t.Errorf("responseGoTypeName(%q, %d, %v) = %q, want %q",
					tc.method, tc.status, tc.isNoContent, got, tc.want)
			}
		})
	}
}

// TestLiftHTTPResponses covers projection of contract.yaml http.responses[]
// into the IR Responses slice — sort order, success/error split, NoContent
// flag, and GoTypeName derivation.
func TestLiftHTTPResponses(t *testing.T) {
	t.Parallel()

	t.Run("success + errors sorted", func(t *testing.T) {
		t.Parallel()
		got, err := liftHTTPResponses(&metadata.HTTPTransportMeta{
			SuccessStatus: 200,
			Responses: map[int]metadata.HTTPResponseMeta{
				503: {SchemaRef: "../err.json", Description: "svc down"},
				401: {SchemaRef: "../err.json", Description: "no auth"},
			},
		}, "Get", "http.test.sorted.v1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("len = %d, want 3", len(got))
		}
		// Sorted ascending: 200, 401, 503.
		want := []int{200, 401, 503}
		for i, s := range want {
			if got[i].Status != s {
				t.Errorf("[%d].Status = %d, want %d", i, got[i].Status, s)
			}
		}
		// Success entry: IsError=false, IsNoContent=false (200 with body).
		if got[0].IsError || got[0].IsNoContent {
			t.Errorf("success: IsError=%v IsNoContent=%v", got[0].IsError, got[0].IsNoContent)
		}
		// Error entries: IsError=true.
		if !got[1].IsError || !got[2].IsError {
			t.Errorf("errors not flagged")
		}
		if got[2].GoTypeName != "Get503ErrorResponse" {
			t.Errorf("err GoTypeName = %q", got[2].GoTypeName)
		}
	})

	t.Run("204 NoContent flagged", func(t *testing.T) {
		t.Parallel()
		// C18 tighten: even NoContent endpoints must declare at least one 4xx/5xx.
		got, err := liftHTTPResponses(&metadata.HTTPTransportMeta{
			SuccessStatus: 204,
			NoContent:     true,
			Responses: map[int]metadata.HTTPResponseMeta{
				400: {Description: "Bad Request"},
			},
		}, "Delete", "http.test.nocontent.v1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2 (204 + 400)", len(got))
		}
		if !got[0].IsNoContent {
			t.Errorf("204 success IsNoContent must be true")
		}
		if got[0].GoTypeName != "Delete204NoContentResponse" {
			t.Errorf("GoTypeName = %q", got[0].GoTypeName)
		}
		if got[1].Status != 400 || !got[1].IsError {
			t.Errorf("second entry should be 400 error, got status=%d isError=%v", got[1].Status, got[1].IsError)
		}
	})

	t.Run("no success status and no responses returns error (C18)", func(t *testing.T) {
		t.Parallel()
		// C18: contracts with neither SuccessStatus nor responses[] are rejected.
		_, err := liftHTTPResponses(&metadata.HTTPTransportMeta{}, "Foo", "http.test.empty.v1")
		if err == nil {
			t.Fatal("expected C18 error for contract with no SuccessStatus and no responses[], got nil")
		}
	})

	t.Run("responses-duplicated success status deduped", func(t *testing.T) {
		t.Parallel()
		// Defensive path in liftHTTPResponses: when contract authors
		// accidentally mirror the success status into responses[], the
		// success entry wins (only one ResponseSpec emitted for that status).
		got, err := liftHTTPResponses(&metadata.HTTPTransportMeta{
			SuccessStatus: 200,
			Responses: map[int]metadata.HTTPResponseMeta{
				200: {SchemaRef: "../err.json"}, // duplicate — defensive
				404: {SchemaRef: "../err.json"},
			},
		}, "Get", "http.test.deduped.v1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("len = %d, want 2 (success+404, success-dup deduped)", len(got))
		}
	})
}

// TestBuildHTTPEndpointSpec_ClientsOnlyRequiresInternalPathAndClients verifies the
// three scenarios for auth.clientsOnly validation in buildHTTPEndpointSpec.
func TestBuildHTTPEndpointSpec_ClientsOnlyRequiresInternalPathAndClients(t *testing.T) {
	t.Parallel()

	makeContract := func(path string, clients []string) *metadata.ContractMeta {
		return &metadata.ContractMeta{
			ID:      "http.internal.sample.list.v1",
			Kind:    "http",
			Codegen: true,
			Endpoints: metadata.EndpointsMeta{
				Server:  metadatatest.CellIDTestCell,
				Clients: clients,
				HTTP: &metadata.HTTPTransportMeta{
					Method:        "GET",
					Path:          path,
					SuccessStatus: 200,
					NoContent:     false,
					Auth:          metadata.HTTPAuthMeta{ClientsOnly: true},
					Responses: map[int]metadata.HTTPResponseMeta{
						400: {Description: "Bad Request", SchemaRef: "err.json"},
					},
				},
			},
		}
	}

	t.Run("clientsOnly + internal path + clients non-empty passes", func(t *testing.T) {
		t.Parallel()
		contract := makeContract("/internal/v1/sample/list", []string{"testcell"})
		http := contract.Endpoints.HTTP
		pathParams := buildPathParams(http)
		queryParams := buildQueryParams(http)
		spec, err := buildHTTPEndpointSpec(contract, http, pathParams, queryParams)
		if err != nil {
			t.Fatalf("expected no error for valid clientsOnly config, got: %v", err)
		}
		if !spec.AuthClientsOnly {
			t.Error("AuthClientsOnly should be true")
		}
	})

	t.Run("clientsOnly + api path returns error", func(t *testing.T) {
		t.Parallel()
		contract := makeContract("/api/v1/sample/list", []string{"testcell"})
		http := contract.Endpoints.HTTP
		pathParams := buildPathParams(http)
		queryParams := buildQueryParams(http)
		_, err := buildHTTPEndpointSpec(contract, http, pathParams, queryParams)
		if err == nil {
			t.Fatal("expected error for clientsOnly on non-internal path")
		}
		if !strings.Contains(err.Error(), "internal path") {
			t.Errorf("error should mention 'internal path', got: %v", err)
		}
	})

	t.Run("clientsOnly + internal path + clients empty returns error", func(t *testing.T) {
		t.Parallel()
		contract := makeContract("/internal/v1/sample/list", nil)
		http := contract.Endpoints.HTTP
		pathParams := buildPathParams(http)
		queryParams := buildQueryParams(http)
		_, err := buildHTTPEndpointSpec(contract, http, pathParams, queryParams)
		if err == nil {
			t.Fatal("expected error for clientsOnly with empty clients")
		}
		if !strings.Contains(err.Error(), "clients is empty") {
			t.Errorf("error should mention 'clients is empty', got: %v", err)
		}
	})

	t.Run("clientsOnly + exclusive auth mode returns error", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name string
			auth metadata.HTTPAuthMeta
		}{
			{name: "public", auth: metadata.HTTPAuthMeta{ClientsOnly: true, Public: true}},
			{name: "bootstrap", auth: metadata.HTTPAuthMeta{ClientsOnly: true, Bootstrap: true}},
			{name: "passwordResetExempt", auth: metadata.HTTPAuthMeta{ClientsOnly: true, PasswordResetExempt: true}},
		}
		for _, tc := range cases {
			tc := tc
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				contract := makeContract("/internal/v1/sample/list", []string{"testcell"})
				http := contract.Endpoints.HTTP
				http.Auth = tc.auth
				pathParams := buildPathParams(http)
				queryParams := buildQueryParams(http)
				_, err := buildHTTPEndpointSpec(contract, http, pathParams, queryParams)
				if err == nil {
					t.Fatal("expected error for clientsOnly combined with exclusive auth mode")
				}
				if !strings.Contains(err.Error(), "auth.clientsOnly:true") {
					t.Errorf("error should mention auth.clientsOnly:true, got: %v", err)
				}
			})
		}
	})
}
