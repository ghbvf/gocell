package contractgen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ghbvf/gocell/kernel/governance"
)

// bundleSchemaRefs resolves all external $ref entries in raw (a JSON Schema file
// at schemaPath inside rootDir) into a single self-contained document with no
// remaining $ref nodes. The result can be embedded as a Go string literal and
// compiled by schemavalidate.NewValidator (santhosh-tekuri, base URI "mem:///",
// no external loader) without errors.
//
// Behavior:
//   - If raw contains no `"$ref"` substring, raw is returned unchanged (preserves
//     current behavior for all non-$ref contracts and avoids unnecessary JSON
//     round-trips).
//   - Otherwise every node whose only key is `"$ref"` is replaced in-place by the
//     referenced file's content, with top-level $ keys and "title" stripped so they
//     do not pollute the embedding context.
//   - $ref values that are absolute URLs (http:// / https://) or absolute file paths
//     are rejected.
//   - $ref values that resolve outside rootDir are rejected (path-traversal guard).
//   - A visited set (keyed by absolute path) prevents infinite loops on circular refs.
func bundleSchemaRefs(rootDir, schemaPath string, raw []byte) ([]byte, error) {
	if !bytes.Contains(raw, []byte(`"$ref"`)) {
		return raw, nil
	}

	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("refbundle: parse %s: %w", schemaPath, err)
	}

	visited := make(map[string]struct{})
	result, err := resolveNode(rootDir, schemaPath, doc, visited)
	if err != nil {
		return nil, err
	}

	out, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("refbundle: marshal result: %w", err)
	}
	return out, nil
}

// resolveNode recursively walks a decoded JSON node, replacing $ref objects with
// the inlined content of the referenced file (with $ and title keys stripped).
func resolveNode(rootDir, currentFile string, node any, visited map[string]struct{}) (any, error) {
	switch v := node.(type) {
	case map[string]any:
		return resolveObject(rootDir, currentFile, v, visited)
	case []any:
		return resolveArray(rootDir, currentFile, v, visited)
	default:
		return node, nil
	}
}

// resolveObject handles a JSON object node, replacing it wholesale if it contains
// a "$ref" key, or recursing into each value otherwise.
func resolveObject(rootDir, currentFile string, obj map[string]any, visited map[string]struct{}) (any, error) {
	ref, hasRef := obj["$ref"]
	if !hasRef {
		return resolveObjectFields(rootDir, currentFile, obj, visited)
	}

	refStr, ok := ref.(string)
	if !ok {
		return nil, fmt.Errorf("refbundle: $ref must be a string in %s", currentFile)
	}
	return loadAndStripRef(rootDir, currentFile, refStr, visited)
}

// resolveObjectFields recurses into every value of an object that has no $ref.
func resolveObjectFields(rootDir, currentFile string, obj map[string]any, visited map[string]struct{}) (map[string]any, error) {
	out := make(map[string]any, len(obj))
	for k, val := range obj {
		resolved, err := resolveNode(rootDir, currentFile, val, visited)
		if err != nil {
			return nil, err
		}
		out[k] = resolved
	}
	return out, nil
}

// resolveArray recurses into every element of a JSON array.
func resolveArray(rootDir, currentFile string, arr []any, visited map[string]struct{}) ([]any, error) {
	out := make([]any, len(arr))
	for i, elem := range arr {
		resolved, err := resolveNode(rootDir, currentFile, elem, visited)
		if err != nil {
			return nil, err
		}
		out[i] = resolved
	}
	return out, nil
}

// loadAndStripRef validates and loads the referenced file, strips its top-level
// $ keys and "title", and returns the resolved node ready for inlining.
func loadAndStripRef(rootDir, currentFile, refStr string, visited map[string]struct{}) (any, error) {
	if err := validateRefString(refStr, currentFile); err != nil {
		return nil, err
	}

	targetAbs := filepath.Clean(filepath.Join(filepath.Dir(currentFile), refStr))

	if !governance.IsWithinRoot(rootDir, targetAbs) {
		return nil, fmt.Errorf(
			"refbundle: $ref %q in %s resolves to %s which is outside root %s",
			refStr, currentFile, targetAbs, rootDir)
	}

	if _, seen := visited[targetAbs]; seen {
		return nil, fmt.Errorf("refbundle: circular $ref detected: %s", targetAbs)
	}
	visited[targetAbs] = struct{}{}
	defer delete(visited, targetAbs)

	data, err := os.ReadFile(targetAbs) // path validated by IsWithinRoot above
	if err != nil {
		return nil, fmt.Errorf("refbundle: read %s: %w", targetAbs, err)
	}

	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("refbundle: parse %s: %w", targetAbs, err)
	}

	// Recurse so nested $refs in the mixin are also resolved.
	resolved, err := resolveNode(rootDir, targetAbs, doc, visited)
	if err != nil {
		return nil, err
	}

	obj, ok := resolved.(map[string]any)
	if !ok {
		// Non-object mixin (e.g. bare integer schema) — return as-is.
		return resolved, nil
	}

	return stripMetaKeys(obj), nil
}

// validateRefString rejects $ref values that are absolute URLs or absolute paths.
func validateRefString(refStr, currentFile string) error {
	if strings.HasPrefix(refStr, "http://") || strings.HasPrefix(refStr, "https://") {
		return fmt.Errorf("refbundle: $ref absolute URL not supported %q in %s", refStr, currentFile)
	}
	if filepath.IsAbs(refStr) {
		return fmt.Errorf("refbundle: $ref absolute path not supported %q in %s", refStr, currentFile)
	}
	return nil
}

// stripMetaKeys removes top-level keys beginning with "$" and the "title" key
// from the resolved mixin object so they do not pollute the embedding context.
// For example, $schema, $id, $defs, $anchor are stripped; description, type,
// minimum, maximum are preserved.
func stripMetaKeys(obj map[string]any) map[string]any {
	out := make(map[string]any, len(obj))
	for k, v := range obj {
		if strings.HasPrefix(k, "$") || k == "title" {
			continue
		}
		out[k] = v
	}
	return out
}
