package contractgen

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/ghbvf/gocell/framework/kernel/metadata"
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

// contractArtifacts is the kind × artifact matrix. webhook and grpc are
// intentionally absent (zero contractgen artifacts by design): webhook wires via
// cellgen ReceiverSpec literals; grpc's server contract is buf's generated
// pb.<Svc>Server interface (the ADR-202605260000 D5 proto-derived Hard funnel) —
// contractgen would only emit a redundant, register-incompatible parallel
// interface that also collides on package name with the pb.go in the same dir,
// so it emits nothing (#1688). projection needs no special-casing — it is
// covered by the types/iface kind sets.
var contractArtifacts = []artifactDef{
	{"types.tmpl", "types_gen.go", []string{"http", "event", "command", "projection", "saga"}},
	{"iface.tmpl", "iface_gen.go", []string{"http", "event", "projection", "saga"}},
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

	// The barrel index.ts is derived from the FULL project (every codegen
	// contract that emits TS), NOT the generate scope — so a scoped
	// `generate contract <id>` writes the same index.ts a full run would, and the
	// generatedverify manifest (which also calls RenderTSBarrel) stays
	// byte-identical. Without this, a scoped run would clobber the barrel with
	// only the scoped contract's entry.
	if err := generateTSBarrel(root, p, opts, &res); err != nil {
		return res, err
	}

	return res, nil
}

// checkGRPCProtoCollisions builds a protoRegistry from every grpc contract in p
// (reading each .proto for package/import identity) and fails fast on a
// duplicate (proto package, service) or a divergent import path for the
// same proto service. The single source of proto identity is the .proto file
// (GRPC-PROTO-REGISTRY-SINGLE-SOURCE-01).
//
// Since #1688 deleted contractgen's grpc per-contract spec (kind=grpc now emits
// zero artifacts — buf's pb.<Svc>Server is the sole server contract), this
// pre-pass is the ONLY contractgen proto-read/validation point: validateGRPCProtoPath
// + ReadProtoServiceInfo here fail-close every codegen:true grpc contract's proto
// (path under contracts/grpc/, exported RPC identifiers — unary or streaming,
// PR-10 #1153 — valid go_package) before any generation runs. Governance FMT-37
// is the parallel gate in `gocell validate`.
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
		// Sole grpc proto validation gate (#1688): applies the proto-path guards +
		// ReadProtoServiceInfo fail-closed for every codegen:true grpc contract.
		if err := validateGRPCProtoPath(id, g.Proto); err != nil {
			return err
		}
		// Resolve module-relative proto ("contracts/grpc/…") to a repo-root-relative
		// path so satellite-module contracts (examples/iotdevice) read the proto at
		// "<moduleBase>/contracts/grpc/…" rather than the wrong repo-root location (#1151).
		protoRel := metadata.GRPCProtoRepoRelPath(p.Contracts[id].File, g.Proto)
		info, err := ReadProtoServiceInfo(filepath.Join(root, filepath.FromSlash(protoRel)), g.Service)
		if err != nil {
			return fmt.Errorf("contract %q: %w", id, err)
		}
		// Per-method public overlay referential integrity (#1675): each
		// endpoints.grpc.methods[] name must be a member of the proto's method set.
		// kernel/governance cannot read the .proto (kernel⊥tools), so this codegen
		// pre-pass is the Hard funnel gate; FMT-41 owns the metadata-pure guards.
		if err := validateGRPCMethodOverlay(id, g.Service, g.Methods, info); err != nil {
			return err
		}
		if err := reg.register(id, g.Service, info); err != nil {
			return err
		}
	}
	return nil
}

// validateGRPCMethodOverlay enforces that every endpoints.grpc.methods[] overlay
// entry (#1675) names an RPC that actually exists in the proto service. The
// overlay only annotates proto methods (it never declares the method set, which
// stays single-sourced from the .proto, #1655); a stale or typo'd name is a wiring
// bug that must fail generation rather than emit an inert PublicMethods entry.
func validateGRPCMethodOverlay(contractID, service string, methods []metadata.GRPCMethodMeta, info ProtoServiceInfo) error {
	if len(methods) == 0 {
		return nil
	}
	protoMethods := make(map[string]struct{}, len(info.Methods))
	for _, pm := range info.Methods {
		protoMethods[pm.Name] = struct{}{}
	}
	for _, m := range methods {
		if _, ok := protoMethods[m.Name]; !ok {
			// Name the contract + the service FQN exactly as the author wrote it in
			// endpoints.grpc.service (NOT the proto package), and point at the fix.
			return fmt.Errorf("contract %q: endpoints.grpc.methods entry %q is not an RPC of proto service %q; "+
				"check the method name against the .proto service's rpc declarations (or remove the overlay entry)",
				contractID, m.Name, service)
		}
	}
	return nil
}

// generateOneContract renders all artifacts for a single contract and writes
// (or dry-runs / verifies) them to disk, appending outcomes to res. The kind ×
// artifact matrix is driven by artifactsForKind (single source, shared with
// RenderContractArtifacts). The barrel index.ts is NOT emitted here — it is a
// full-project aggregate written once by Generate (see generateTSBarrel).
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

	// webhook / grpc: recognized, zero contractgen artifacts by design. webhook
	// registration uses kernel/webhook.ReceiverSpec literals via cellgen; grpc's
	// server contract is buf's generated pb.<Svc>Server (#1688) — neither has a
	// per-contract contractgen package.
	if spec.Kind == "webhook" || spec.Kind == "grpc" {
		slog.Debug("contractgen: contract emits zero artifacts by design; wiring derives via cellgen / buf",
			"contractID", contractID, "kind", spec.Kind)
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

	// TS emit: per-contract types.ts in generated-ts/ (separate from Go
	// generated/). responseProjection, non-types kinds and empty-DTO specs are
	// skipped by specEmitsTS; the cross-contract barrel is written separately by
	// Generate.
	if !specEmitsTS(spec) {
		return nil
	}
	if err := emitTSTypes(root, spec, opts, res); err != nil {
		return fmt.Errorf("contract %q: ts emit: %w", contractID, err)
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

	// webhook / grpc: recognized, zero contractgen artifacts by design. webhook
	// registration uses kernel/webhook.ReceiverSpec literals via cellgen; grpc's
	// server contract is buf's generated pb.<Svc>Server (#1688) — neither has a
	// per-contract contractgen package.
	if spec.Kind == "webhook" || spec.Kind == "grpc" {
		slog.Debug("contractgen: contract emits zero artifacts by design; wiring derives via cellgen / buf",
			"contractID", contractID, "kind", spec.Kind)
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

	// Per-contract types.ts under generated-ts/ — included in the manifest so the
	// generatedverify reverse-enumeration (which now lists *.ts) finds it in the
	// expected set. Byte-identical to the disk write (both call renderTS(spec)).
	// The cross-contract barrel index.ts is a separate aggregate (RenderTSBarrel).
	out, err = appendTSArtifact(out, root, spec)
	if err != nil {
		return nil, err
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
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %s escapes root %s", abs, root)
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
