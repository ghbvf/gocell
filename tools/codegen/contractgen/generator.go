package contractgen

import (
	"fmt"
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
	// OnlyContract, when non-empty, restricts generation to a single contract id.
	// The contract must have Codegen=true; empty means all opted-in metadata.
	OnlyContract string
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
func Generate(root string, p *metadata.ProjectMeta, opts Options) (Result, error) {
	var res Result
	if root == "" {
		return res, fmt.Errorf("contractgen generate: root is empty")
	}
	if p == nil {
		return res, fmt.Errorf("contractgen generate: project is nil")
	}

	contractIDs, err := selectContractIDs(p, opts.OnlyContract)
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

	return out, nil
}

// selectContractIDs returns the ordered list of contract IDs to process.
// If only is non-empty, validates it exists and has Codegen=true.
// If only is empty, returns all Codegen=true contracts sorted by ID.
func selectContractIDs(p *metadata.ProjectMeta, only string) ([]string, error) {
	if only != "" {
		contract, ok := p.Contracts[only]
		if !ok {
			return nil, fmt.Errorf("contractgen generate: contract %q not found", only)
		}
		if !contract.Codegen {
			return nil, fmt.Errorf("contractgen generate: contract %q has codegen=false", only)
		}
		return []string{only}, nil
	}

	var ids []string
	for id, c := range p.Contracts {
		if c.Codegen {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids, nil
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
