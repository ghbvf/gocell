package metadata

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SchemaRefScope describes the filesystem boundary used to resolve a schema ref.
type SchemaRefScope string

const (
	// SchemaRefScopeContractDir keeps schemaRefs.* inside the owning contract dir.
	SchemaRefScopeContractDir SchemaRefScope = "contractDir"
	// SchemaRefScopeProjectRoot permits HTTP response schemas to point at shared
	// schemas elsewhere in the repository.
	SchemaRefScopeProjectRoot SchemaRefScope = "projectRoot"
)

// ContractSchemaRef is one schema reference declared by a contract.
type ContractSchemaRef struct {
	Field string
	Ref   string
	Scope SchemaRefScope
}

// ResolvedSchemaRef is a schema ref resolved against an absolute project root.
type ResolvedSchemaRef struct {
	ContractSchemaRef
	AbsPath    string
	ProjectRel string
}

// SchemaRefError reports a schema ref resolution failure.
type SchemaRefError struct {
	Field string
	Ref   string
	Kind  string
}

func (e *SchemaRefError) Error() string {
	return fmt.Sprintf("%s %q: %s", e.Field, e.Ref, e.Kind)
}

// ContractDirFromMeta returns the contract directory relative to the project root.
func ContractDirFromMeta(c *ContractMeta) string {
	if c == nil {
		return ""
	}
	if c.Dir != "" {
		return filepath.ToSlash(c.Dir)
	}
	return ContractDirFromID(c.ID)
}

// ContractDirFromID converts a contract ID to its default directory path.
func ContractDirFromID(id string) string {
	segments := strings.Split(id, ".")
	return filepath.ToSlash(filepath.Join("contracts", filepath.Join(segments...)))
}

// ContractSchemaRefs returns every schema reference declared by c in deterministic
// order. Empty refs are included so callers that need field-level completeness can
// still inspect them.
func ContractSchemaRefs(c *ContractMeta) []ContractSchemaRef {
	if c == nil {
		return nil
	}
	refs := []ContractSchemaRef{
		{Field: "schemaRefs.request", Ref: c.SchemaRefs.Request, Scope: SchemaRefScopeContractDir},
		{Field: "schemaRefs.response", Ref: c.SchemaRefs.Response, Scope: SchemaRefScopeContractDir},
		{Field: "schemaRefs.payload", Ref: c.SchemaRefs.Payload, Scope: SchemaRefScopeContractDir},
		{Field: "schemaRefs.headers", Ref: c.SchemaRefs.Headers, Scope: SchemaRefScopeContractDir},
	}
	for _, key := range sortedStringKeys(c.SchemaRefs.Extra) {
		refs = append(refs, ContractSchemaRef{
			Field: "schemaRefs." + key,
			Ref:   c.SchemaRefs.Extra[key],
			Scope: SchemaRefScopeContractDir,
		})
	}
	if c.Endpoints.HTTP != nil {
		statuses := make([]int, 0, len(c.Endpoints.HTTP.Responses))
		for status := range c.Endpoints.HTTP.Responses {
			statuses = append(statuses, status)
		}
		sort.Ints(statuses)
		for _, status := range statuses {
			resp := c.Endpoints.HTTP.Responses[status]
			refs = append(refs, ContractSchemaRef{
				Field: fmt.Sprintf("endpoints.http.responses[%d].schemaRef", status),
				Ref:   resp.SchemaRef,
				Scope: SchemaRefScopeProjectRoot,
			})
		}
	}
	return refs
}

// ResolveContractSchemaRef resolves ref relative to c's contract directory and
// verifies that the target stays inside the ref's declared scope.
func ResolveContractSchemaRef(projectRoot string, c *ContractMeta, ref ContractSchemaRef) (ResolvedSchemaRef, error) {
	if ref.Ref == "" {
		return ResolvedSchemaRef{ContractSchemaRef: ref}, nil
	}
	if projectRoot == "" {
		return ResolvedSchemaRef{}, &SchemaRefError{Field: ref.Field, Ref: ref.Ref, Kind: "project root is required"}
	}
	refPath := filepath.FromSlash(ref.Ref)
	if filepath.IsAbs(refPath) {
		return ResolvedSchemaRef{}, &SchemaRefError{Field: ref.Field, Ref: ref.Ref, Kind: "absolute paths are not allowed"}
	}
	contractDir := ContractDirFromMeta(c)
	if contractDir == "" {
		return ResolvedSchemaRef{}, &SchemaRefError{Field: ref.Field, Ref: ref.Ref, Kind: "contract directory is unknown"}
	}
	rootAbs, err := filepath.Abs(projectRoot)
	if err != nil {
		return ResolvedSchemaRef{}, &SchemaRefError{Field: ref.Field, Ref: ref.Ref, Kind: err.Error()}
	}
	contractAbs := filepath.Join(rootAbs, filepath.FromSlash(contractDir))
	target := filepath.Join(contractAbs, refPath)
	bounds := contractAbs
	if ref.Scope == SchemaRefScopeProjectRoot {
		bounds = rootAbs
	}
	if !isWithinRoot(bounds, target) {
		return ResolvedSchemaRef{}, &SchemaRefError{Field: ref.Field, Ref: ref.Ref, Kind: "path escapes project root or " + string(ref.Scope)}
	}
	rel, err := filepath.Rel(rootAbs, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ResolvedSchemaRef{}, &SchemaRefError{Field: ref.Field, Ref: ref.Ref, Kind: "path escapes project root"}
	}
	return ResolvedSchemaRef{
		ContractSchemaRef: ref,
		AbsPath:           target,
		ProjectRel:        filepath.ToSlash(rel),
	}, nil
}

// ResolveContractSchemaRefs resolves every non-empty schema reference for c.
func ResolveContractSchemaRefs(projectRoot string, c *ContractMeta) ([]ResolvedSchemaRef, error) {
	var out []ResolvedSchemaRef
	for _, ref := range ContractSchemaRefs(c) {
		if ref.Ref == "" {
			continue
		}
		resolved, err := ResolveContractSchemaRef(projectRoot, c, ref)
		if err != nil {
			return nil, err
		}
		out = append(out, resolved)
	}
	return out, nil
}

func sortedStringKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func isWithinRoot(root, target string) bool {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(absRoot); err == nil {
		absRoot = resolved
	} else {
		absRoot = evalExistingPrefix(absRoot)
	}
	if resolved, err := filepath.EvalSymlinks(absTarget); err == nil {
		absTarget = resolved
	} else {
		absTarget = evalExistingPrefix(absTarget)
	}
	prefix := absRoot + string(os.PathSeparator)
	return absTarget == absRoot || strings.HasPrefix(absTarget, prefix)
}

func evalExistingPrefix(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	parent := filepath.Dir(path)
	if parent == path {
		return path
	}
	return filepath.Join(evalExistingPrefix(parent), filepath.Base(path))
}
