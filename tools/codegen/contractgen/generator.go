package contractgen

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/codegen"
)

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

// Generate orchestrates BuildContractSpec → render → write for one or all
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
		return res, fmt.Errorf("contractgen generate: root is empty")
	}
	if p == nil {
		return res, fmt.Errorf("contractgen generate: project is nil")
	}
	if opts.Scope == nil {
		return res, fmt.Errorf("contractgen generate: Scope is required; use ScopeAll{} for all contracts")
	}

	contractIDs, err := selectContractIDsByScope(p, opts)
	if err != nil {
		return res, err
	}

	for _, id := range contractIDs {
		if err := generateOneContract(root, p, id, opts, &res); err != nil {
			return res, err
		}
	}
	return res, nil
}

// generateOneContract renders all artifacts for a single contract and writes
// (or dry-runs / verifies) them to disk, appending outcomes to res.
//
// Cognitive complexity is intrinsic to the orchestrator: 5 contract kinds ×
// dry-run/write/verify branches × per-artifact emit (types / iface / handler /
// spec / subscription). Splitting would only push the same matrix into helpers.
//
//nolint:gocognit // structural orchestration; see godoc above.
func generateOneContract(root string, p *metadata.ProjectMeta, contractID string, opts Options, res *Result) error {
	// B.5: contract ID sanity — must not contain path separators or traversal sequences.
	if strings.Contains(contractID, "..") || strings.ContainsAny(contractID, `/\`) {
		return fmt.Errorf("contract %q: id contains illegal path characters", contractID)
	}

	spec, err := BuildContractSpec(root, p, contractID)
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

	// For kind=command and kind=projection only types_gen.go + iface_gen.go are
	// emitted (no handler/spec/subscription). This keeps the closed-set valid
	// while full generators are pending.
	if spec.Kind == "command" || spec.Kind == "projection" {
		slog.Warn("contractgen: kind in closed set but no full generator yet; only types/iface emitted",
			"contractID", contractID, "kind", spec.Kind)
	}

	// types_gen.go — always generated.
	typesPath := filepath.Join(pkgDir, "types_gen.go")
	errPfxTypes := "contractgen generate: render types " + contractID
	if err := renderWriteContract(root, "types.tmpl", spec, typesPath, opts, res, errPfxTypes); err != nil {
		return err
	}

	// iface_gen.go — always generated.
	ifacePath := filepath.Join(pkgDir, "iface_gen.go")
	errPfxIface := "contractgen generate: render iface " + contractID
	if err := renderWriteContract(root, "iface.tmpl", spec, ifacePath, opts, res, errPfxIface); err != nil {
		return err
	}

	// handler_gen.go — only for kind=http.
	if spec.Kind == "http" {
		handlerPath := filepath.Join(pkgDir, "handler_gen.go")
		errPfxHandler := "contractgen generate: render handler " + contractID
		if err := renderWriteContract(root, "handler.tmpl", spec, handlerPath, opts, res, errPfxHandler); err != nil {
			return err
		}
	}

	// spec_gen.go + subscription_gen.go — only for kind=event.
	if spec.Kind == "event" {
		specPath := filepath.Join(pkgDir, "spec_gen.go")
		errPfxSpec := "contractgen generate: render spec " + contractID
		if err := renderWriteContract(root, "spec.tmpl", spec, specPath, opts, res, errPfxSpec); err != nil {
			return err
		}

		subPath := filepath.Join(pkgDir, "subscription_gen.go")
		errPfxSub := "contractgen generate: render subscription " + contractID
		if err := renderWriteContract(root, "subscription.tmpl", spec, subPath, opts, res, errPfxSub); err != nil {
			return err
		}
	}

	return nil
}

// renderWriteContract renders one template to content, then writes (or
// dry-runs / verifies) to path, recording the outcome in res.
func renderWriteContract(root, tmplName string, spec *ContractGenSpec, path string, opts Options, res *Result, errPrefix string) error {
	content, err := codegen.Render(codegen.RenderOptions{
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
// Mirrors generateOneContract on the same kind × artifact matrix; the high
// cognitive complexity is structural orchestration, not nested business logic.
//
//nolint:gocognit,cyclop,funlen // structural orchestration; see godoc above.
func RenderContractArtifacts(root string, p *metadata.ProjectMeta, contractID string) ([]CodegenArtifact, error) {
	if p == nil {
		return nil, fmt.Errorf("contractgen render artifacts: project is nil")
	}
	contract, ok := p.Contracts[contractID]
	if !ok {
		return nil, fmt.Errorf("contractgen render artifacts: contract %q not found", contractID)
	}
	if !contract.Codegen {
		return nil, nil
	}

	spec, err := BuildContractSpec(root, p, contractID)
	if err != nil {
		return nil, err
	}

	pkgDir := filepath.Join(root, filepath.FromSlash(spec.PackagePath))

	var out []CodegenArtifact

	// types_gen.go
	typesPath := filepath.Join(pkgDir, "types_gen.go")
	typesContent, err := codegen.Render(codegen.RenderOptions{
		TemplateName: "types.tmpl",
		Templates:    templates,
		Data:         spec,
		Filename:     typesPath,
	})
	if err != nil {
		return nil, fmt.Errorf("contractgen render artifacts: %q types: %w", contractID, err)
	}
	typesRel, err := relFromRoot(root, typesPath)
	if err != nil {
		return nil, err
	}
	out = append(out, CodegenArtifact{Path: typesRel, Content: typesContent})

	// iface_gen.go
	ifacePath := filepath.Join(pkgDir, "iface_gen.go")
	ifaceContent, err := codegen.Render(codegen.RenderOptions{
		TemplateName: "iface.tmpl",
		Templates:    templates,
		Data:         spec,
		Filename:     ifacePath,
	})
	if err != nil {
		return nil, fmt.Errorf("contractgen render artifacts: %q iface: %w", contractID, err)
	}
	ifaceRel, err := relFromRoot(root, ifacePath)
	if err != nil {
		return nil, err
	}
	out = append(out, CodegenArtifact{Path: ifaceRel, Content: ifaceContent})

	// handler_gen.go — only for kind=http.
	if spec.Kind == "http" {
		handlerPath := filepath.Join(pkgDir, "handler_gen.go")
		handlerContent, err := codegen.Render(codegen.RenderOptions{
			TemplateName: "handler.tmpl",
			Templates:    templates,
			Data:         spec,
			Filename:     handlerPath,
		})
		if err != nil {
			return nil, fmt.Errorf("contractgen render artifacts: %q handler: %w", contractID, err)
		}
		handlerRel, err := relFromRoot(root, handlerPath)
		if err != nil {
			return nil, err
		}
		out = append(out, CodegenArtifact{Path: handlerRel, Content: handlerContent})
	}

	// spec_gen.go + subscription_gen.go — only for kind=event.
	if spec.Kind == "event" {
		specPath := filepath.Join(pkgDir, "spec_gen.go")
		specContent, err := codegen.Render(codegen.RenderOptions{
			TemplateName: "spec.tmpl",
			Templates:    templates,
			Data:         spec,
			Filename:     specPath,
		})
		if err != nil {
			return nil, fmt.Errorf("contractgen render artifacts: %q spec: %w", contractID, err)
		}
		specRel, err := relFromRoot(root, specPath)
		if err != nil {
			return nil, err
		}
		out = append(out, CodegenArtifact{Path: specRel, Content: specContent})

		subPath := filepath.Join(pkgDir, "subscription_gen.go")
		subContent, err := codegen.Render(codegen.RenderOptions{
			TemplateName: "subscription.tmpl",
			Templates:    templates,
			Data:         spec,
			Filename:     subPath,
		})
		if err != nil {
			return nil, fmt.Errorf("contractgen render artifacts: %q subscription: %w", contractID, err)
		}
		subRel, err := relFromRoot(root, subPath)
		if err != nil {
			return nil, err
		}
		out = append(out, CodegenArtifact{Path: subRel, Content: subContent})
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
			return nil, fmt.Errorf("contractgen generate: contract %q not found", id)
		}
		if !contract.Codegen {
			return nil, fmt.Errorf("contractgen generate: contract %q has codegen=false", id)
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
