package contractgen

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/codegen"
)

// Error message prefixes for the two contract rendering entry points. Each names
// a full error namespace: errPrefixGenerate covers every generate-side message
// (Generate guards, scope selection, and per-artifact render in
// generateOneContract); errPrefixRender covers every RenderContractArtifacts
// message.
const (
	errPrefixGenerate = "contractgen generate: "
	errPrefixRender   = "contractgen render artifacts: "
)

// artifactDef is one row of the contract kind × artifact matrix: a template, its
// generated output file, and the contract kinds that emit it. contractArtifacts
// (below) is the single source consumed by both generateOneContract (disk write)
// and RenderContractArtifacts (in-memory); the human-readable table lives in
// doc.go. Slice order is the emit/append order — RenderContractArtifacts returns
// artifacts in this order (consumed by cellgen/generatedverify), so reordering
// changes wire behavior and must regenerate goldens.
type artifactDef struct {
	template string   // e.g. "types.tmpl"
	file     string   // generated output filename, e.g. "types_gen.go"
	kinds    []string // contract kinds that emit this artifact
}

// word returns the artifact noun used in error messages (the file stem),
// e.g. "types" for "types_gen.go".
func (a artifactDef) word() string { return strings.TrimSuffix(a.file, "_gen.go") }

// contractArtifacts is the kind × artifact matrix. webhook is intentionally
// absent (zero artifacts by design); projection and grpc need no special-casing
// — they are covered by the types/iface kind sets.
var contractArtifacts = []artifactDef{
	{"types.tmpl", "types_gen.go", []string{"http", "event", "command", "projection", "grpc", "saga"}},
	{"iface.tmpl", "iface_gen.go", []string{"http", "event", "projection", "grpc", "saga"}},
	{"handler.tmpl", "handler_gen.go", []string{"http"}},
	{"spec.tmpl", "spec_gen.go", []string{"event"}},
	{"subscription.tmpl", "subscription_gen.go", []string{"event"}},
	{"projection.tmpl", "projection_gen.go", []string{"event"}},
	{"saga.tmpl", "saga_gen.go", []string{"saga"}},
	{"command.tmpl", "command_gen.go", []string{"command"}},
}

// artifactsForKind returns the artifacts emitted for a contract kind, in matrix
// order. An unrecognized kind (including webhook) yields nil.
func artifactsForKind(kind string) []artifactDef {
	var out []artifactDef
	for _, a := range contractArtifacts {
		if slices.Contains(a.kinds, kind) {
			out = append(out, a)
		}
	}
	return out
}

// Options controls Generate behavior. Mirrors cellgen.Options.
type Options struct {
	// DryRun emits ActionWouldWrite without filesystem mutation.
	DryRun bool
	// Verify diffs the rendered content against disk and reports drift.
	// Mutually exclusive with DryRun at the CLI layer; combining them here
	// is harmless — Verify dominates (no write either way).
	Verify bool
	// Scope controls which contracts are processed. When nil, Generate returns
	// an error (fail-fast). Use ScopeAll{} for the default "all Codegen=true"
	// behavior, ScopeContracts for a specific ID list, or ScopeCell to restrict
	// to one cell's contracts.
	Scope Scope
	// ModulePath is the consuming repo's Go module path (from its go.mod),
	// threaded to the formatter so generated files group module-local imports
	// the way the target repo's golangci-lint gate expects (#1083). Required:
	// Generate rejects an empty ModulePath. The CLI resolves it via
	// resolveModule (flag-or-go.mod); RenderContractArtifacts resolves it from
	// root itself.
	ModulePath string
}

// Result reports the outcome of Generate.
type Result struct {
	// Generated lists files that were written, would-have-been-written
	// (DryRun), or remain unchanged (Unchanged).
	Generated []string
	// Drifted lists files whose disk content differs from the freshly
	// rendered content (Verify mode only).
	Drifted []string
}

// GeneratedFiles satisfies the cmd/gocell/app.CodegenResult interface.
func (r Result) GeneratedFiles() []string { return r.Generated }

// DriftedFiles satisfies the cmd/gocell/app.CodegenResult interface.
func (r Result) DriftedFiles() []string { return r.Drifted }

// CodegenArtifact is one rendered file (in-memory).
type CodegenArtifact struct {
	// Path is the repo-relative target path,
	// e.g. "generated/contracts/http/order/create/v1/types_gen.go".
	Path string
	// Content is the rendered, formatted, goimports-processed bytes.
	Content []byte
}

// Generate orchestrates buildContractSpec → render → write for one or all
// opted-in metadata.
// root is the repository root (absolute path; from which go.mod is read for
// module path).
//
// opts.Scope must be non-nil. Use ScopeAll{} for the default "all Codegen=true"
// behavior, ScopeContracts for a specific ID list, or ScopeCell to restrict to
// one cell's contracts. A nil Scope is rejected with an error.
func Generate(root string, p *metadata.ProjectMeta, opts Options) (Result, error) {
	var res Result
	if root == "" {
		return res, fmt.Errorf(errPrefixGenerate + "root is empty")
	}
	if p == nil {
		return res, fmt.Errorf(errPrefixGenerate + "project is nil")
	}
	if opts.Scope == nil {
		return res, fmt.Errorf(errPrefixGenerate + "Scope is required; use ScopeAll{} for all contracts")
	}
	if opts.ModulePath == "" {
		return res, fmt.Errorf(errPrefixGenerate + "ModulePath is required (resolve from go.mod or --module-path)")
	}

	contractIDs, err := selectContractIDsByScope(p, opts)
	if err != nil {
		return res, err
	}

	// Cross-contract gate: (proto package, service) global uniqueness +
	// single import path per proto service. Scans ALL grpc contracts (not just
	// the selected scope) so collisions surface regardless of scope. Iterates the
	// empty set until the first real grpc contract lands (PR 8).
	if err := checkGRPCProtoCollisions(root, p); err != nil {
		return res, err
	}

	for _, id := range contractIDs {
		if err := generateOneContract(root, p, id, opts, &res); err != nil {
			return res, err
		}
	}
	return res, nil
}

// checkGRPCProtoCollisions builds a protoRegistry from every grpc contract in p
// (reading each .proto for package/import identity) and fails fast on a
// duplicate (proto package, service) or a divergent import path for the
// same proto service. The single source of proto identity is the .proto file
// (GRPC-PROTO-REGISTRY-SINGLE-SOURCE-01); this re-reads the protos that
// buildGRPCSpec also reads (codegen is not a hot path), keeping the
// cross-contract gate separate from per-contract spec construction.
func checkGRPCProtoCollisions(root string, p *metadata.ProjectMeta) error {
	reg := newProtoRegistry()
	ids := make([]string, 0, len(p.Contracts))
	for id, c := range p.Contracts {
		// Honor the codegen:false opt-out, same as selectContractIDsByScope: a
		// disabled grpc draft (placeholder/absent proto) must not be read here, or
		// it would block generation of every other contract in the run.
		if c != nil && c.Codegen && c.Kind == "grpc" && c.Endpoints.GRPC != nil {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		g := p.Contracts[id].Endpoints.GRPC
		// This pre-pass runs before generateOneContract → buildGRPCSpec, so it
		// applies the proto-path guards itself (it cannot rely on buildGRPCSpec
		// having validated yet). The proto is read here and again in buildGRPCSpec;
		// codegen is not a hot path and threading a shared registry through
		// buildContractSpec's signature would be more invasive than the re-read.
		if err := validateGRPCProtoPath(id, g.Proto); err != nil {
			return err
		}
		info, err := ReadProtoServiceInfo(filepath.Join(root, filepath.FromSlash(g.Proto)), g.Service)
		if err != nil {
			return fmt.Errorf("contract %q: %w", id, err)
		}
		if err := reg.register(id, g.Service, info); err != nil {
			return err
		}
	}
	return nil
}

// generateOneContract renders all artifacts for a single contract and writes
// (or dry-runs / verifies) them to disk, appending outcomes to res. The kind ×
// artifact matrix is driven by artifactsForKind (single source, shared with
// RenderContractArtifacts).
func generateOneContract(root string, p *metadata.ProjectMeta, contractID string, opts Options, res *Result) error {
	// B.5: contract ID sanity — must not contain path separators or traversal sequences.
	if strings.Contains(contractID, "..") || strings.ContainsAny(contractID, `/\`) {
		return fmt.Errorf("contract %q: id contains illegal path characters", contractID)
	}

	spec, err := buildContractSpec(root, p, contractID)
	if err != nil {
		// B.6: wrap error with contract ID context.
		return fmt.Errorf("contract %q: %w", contractID, err)
	}

	// B.5: package path must be within the generated/contracts/ subtree.
	relPath := filepath.ToSlash(spec.PackagePath)
	if !strings.HasPrefix(relPath, "generated/contracts/") {
		return fmt.Errorf("contract %q: package path %q does not start with generated/contracts/", contractID, relPath)
	}

	pkgDir := filepath.Join(root, filepath.FromSlash(spec.PackagePath))

	// webhook: recognized, zero artifacts by design — registration uses
	// kernel/webhook.ReceiverSpec literals via cellgen, no per-contract package.
	if spec.Kind == "webhook" {
		slog.Debug("contractgen: webhook contract emits zero artifacts by design; wiring derives via cellgen from slice.yaml",
			"contractID", contractID)
		return nil
	}

	// For kind=projection, types_gen.go + iface_gen.go IS the complete product by
	// design: NewProjectionRequest is generated on the event-contract side, and
	// cellgen derives reg.RegisterProjection from event-subscribe CUs.
	if spec.Kind == "projection" {
		slog.Debug("contractgen: projection contract emits types/iface only by design; "+
			"NewProjectionRequest is generated on the event-contract side",
			"contractID", contractID)
	}

	// Per-artifact emit, driven by the kind × artifact matrix. The applicable-kind
	// rules (types always; iface except command; handler only http; spec /
	// subscription / projection only event; saga only saga; command only command)
	// live in contractArtifacts.
	for _, a := range artifactsForKind(spec.Kind) {
		path := filepath.Join(pkgDir, a.file)
		errPrefix := errPrefixGenerate + "render " + a.word() + " " + contractID
		if err := renderWriteContract(root, a.template, spec, path, opts, res, errPrefix); err != nil {
			return err
		}
	}

	return nil
}

// renderWriteContract renders one template to content, then writes (or
// dry-runs / verifies) to path, recording the outcome in res.
func renderWriteContract(root, tmplName string, spec *ContractGenSpec, path string, opts Options, res *Result, errPrefix string) error {
	content, err := codegen.Render(opts.ModulePath, codegen.RenderOptions{
		TemplateName: tmplName,
		Templates:    templates,
		Data:         spec,
		Filename:     path,
	})
	if err != nil {
		return fmt.Errorf("%s: %w", errPrefix, err)
	}

	writeRes, err := codegen.Write(codegen.WriteOptions{
		Path:     path,
		Content:  content,
		RepoRoot: root,
		DryRun:   opts.DryRun,
		Verify:   opts.Verify,
	})
	if err != nil {
		return err
	}
	recordContractResult(res, writeRes)
	return nil
}

// RenderContractArtifacts renders a single contract to in-memory artifacts.
// Used by manifest projection / verify pipelines (mirrors cellgen.RenderCellArtifacts).
// Returns (nil, nil) when the contract is not opted in (Codegen=false).
//
// modulePath is the consuming repo's module path, threaded to the formatter
// (#1083). It is required and supplied by the caller — callers resolve it from
// the real repo's go.mod (the render root may be a staging dir without a
// go.mod, e.g. scaffold staging in cellgen/stage_render.go).
//
// The kind × artifact matrix is driven by artifactsForKind (single source,
// shared with generateOneContract).
func RenderContractArtifacts(root string, p *metadata.ProjectMeta, contractID, modulePath string) ([]CodegenArtifact, error) {
	if p == nil {
		return nil, fmt.Errorf(errPrefixRender + "project is nil")
	}
	if modulePath == "" {
		return nil, fmt.Errorf(errPrefixRender + "modulePath is required (resolve from go.mod or --module-path)")
	}
	contract, ok := p.Contracts[contractID]
	if !ok {
		return nil, fmt.Errorf(errPrefixRender+"contract %q not found", contractID)
	}
	if !contract.Codegen {
		return nil, nil
	}

	spec, err := buildContractSpec(root, p, contractID)
	if err != nil {
		return nil, err
	}

	// webhook: recognized, zero artifacts by design — registration uses
	// kernel/webhook.ReceiverSpec literals via cellgen, no per-contract package.
	if spec.Kind == "webhook" {
		slog.Debug("contractgen: webhook contract emits zero artifacts by design; wiring derives via cellgen from slice.yaml",
			"contractID", contractID)
		return nil, nil
	}

	pkgDir := filepath.Join(root, filepath.FromSlash(spec.PackagePath))

	// Per-artifact emit, driven by the kind × artifact matrix (contractArtifacts);
	// the applicable-kind rules are shared with generateOneContract.
	var out []CodegenArtifact
	for _, a := range artifactsForKind(spec.Kind) {
		path := filepath.Join(pkgDir, a.file)
		content, err := codegen.Render(modulePath, codegen.RenderOptions{
			TemplateName: a.template,
			Templates:    templates,
			Data:         spec,
			Filename:     path,
		})
		if err != nil {
			return nil, fmt.Errorf(errPrefixRender+"%q %s: %w", contractID, a.word(), err)
		}
		rel, err := relFromRoot(root, path)
		if err != nil {
			return nil, err
		}
		out = append(out, CodegenArtifact{Path: rel, Content: content})
	}

	return out, nil
}

// selectContractIDsByScope returns the ordered list of contract IDs to process
// based on opts.Scope.
func selectContractIDsByScope(p *metadata.ProjectMeta, opts Options) ([]string, error) {
	switch s := opts.Scope.(type) {
	case ScopeAll:
		return selectAllCodegenContracts(p)
	case ScopeContracts:
		return selectByContractList(p, []string(s))
	case ScopeCell:
		return selectByCellID(p, string(s)), nil
	default:
		// Unknown Scope implementation — treat as ScopeAll.
		return selectAllCodegenContracts(p)
	}
}

// selectAllCodegenContracts returns all Codegen=true contracts sorted by ID.
func selectAllCodegenContracts(p *metadata.ProjectMeta) ([]string, error) {
	var ids []string
	for id, c := range p.Contracts {
		if c.Codegen {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// selectByContractList validates and returns the given list of contract IDs,
// checking each exists and has Codegen=true.
func selectByContractList(p *metadata.ProjectMeta, ids []string) ([]string, error) {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		contract, ok := p.Contracts[id]
		if !ok {
			return nil, fmt.Errorf(errPrefixGenerate+"contract %q not found", id)
		}
		if !contract.Codegen {
			return nil, fmt.Errorf(errPrefixGenerate+"contract %q has codegen=false", id)
		}
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

// ContractIDsForCell returns all Codegen=true contract IDs owned by cellID
// (server or publisher). The returned slice is sorted for deterministic output.
// Thin exported wrapper around selectByCellID for cross-package callers
// (notably cellgen stage_render.go). No OS calls — safe in depguard scaffold-os-ban scope.
// Has no fallible path; returns a plain slice.
func ContractIDsForCell(p *metadata.ProjectMeta, cellID string) []string {
	return selectByCellID(p, cellID)
}

// selectByCellID returns all Codegen=true contracts whose server/publisher
// cell matches cellID. Has no fallible path; returns a plain slice.
func selectByCellID(p *metadata.ProjectMeta, cellID string) []string {
	var ids []string
	for id, c := range p.Contracts {
		if !c.Codegen {
			continue
		}
		owner := c.Endpoints.Server
		if owner == "" {
			owner = c.Endpoints.Publisher
		}
		if owner == cellID {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// relFromRoot converts an absolute path under root into a slash-separated
// relative path. Returns an error if the path escapes root.
func relFromRoot(root, abs string) (string, error) {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", fmt.Errorf("relpath %s vs %s: %w", abs, root, err)
	}
	return filepath.ToSlash(rel), nil
}

// recordContractResult appends the write outcome to the appropriate slice.
func recordContractResult(res *Result, w codegen.WriteResult) {
	switch w.Action {
	case codegen.ActionDrifted:
		res.Drifted = append(res.Drifted, w.Path)
	default:
		res.Generated = append(res.Generated, w.Path)
	}
}
