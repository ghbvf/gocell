package governance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// Rule ID constants for FMT-20..FMT-23. Extracted so that each rule ID string
// is declared in exactly one place; Sonar code-smell rule S1192 (duplicate
// string literals) no longer fires for these identifiers.
const (
	ruleFMT20 = "FMT-20"
	ruleFMT21 = "FMT-21"
	ruleFMT22 = "FMT-22"
	ruleFMT23 = "FMT-23"
)

// --- FMT-20 (formerly FMT-RESPONSE-STRICT-01) ---

// validateFMTResponseStrict01 scans every HTTP-kind contract's request/response
// JSON schemas. For each "type":"object" node in the schema (recursive), if it
// lacks "additionalProperties": false, a violation is emitted.
//
// Rule ID: FMT-20.
// Severity: Error, IssueRequired.
// ref: k8s.io/apiserver admission/plugin/schema — strict schema validation pattern.
func (v *Validator) validateFMTResponseStrict01() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if c.Kind != "http" {
			continue
		}
		for _, schemaRef := range collectHTTPSchemaPaths(c) {
			absPath := filepath.Join(v.root, schemaRef)
			missing, err := scanSchemaForStrictMissing(absPath)
			if err != nil {
				if os.IsNotExist(err) {
					// Missing schema file is reported by FMT-09 / REF rules; skip here.
					continue
				}
				// Parse/IO errors are definitive FMT-20 violations (fail-closed).
				results = append(results, v.newResult(
					ruleFMT20, SeverityError, IssueInvalid,
					schemaRef, "$",
					fmt.Sprintf("contract %q schema %q failed to parse: %v", c.ID, schemaRef, err),
				))
				continue
			}
			for _, loc := range missing {
				results = append(results, v.newResult(
					ruleFMT20, SeverityError, IssueRequired,
					schemaRef, loc,
					fmt.Sprintf("contract %q schema must declare additionalProperties:false at %s", c.ID, loc),
				))
			}
		}
	}
	return results
}

// collectHTTPSchemaPaths returns the relative schema paths for an HTTP contract,
// resolved relative to the project root. It includes the top-level request and
// response refs from schemaRefs, plus the per-status schemaRef from
// endpoints.http.responses[*] so FMT-20 also covers error response schemas.
func collectHTTPSchemaPaths(c *metadata.ContractMeta) []string {
	var paths []string
	if c.SchemaRefs.Request != "" {
		paths = append(paths, filepath.Join(c.Dir, c.SchemaRefs.Request))
	}
	if c.SchemaRefs.Response != "" {
		paths = append(paths, filepath.Join(c.Dir, c.SchemaRefs.Response))
	}
	if c.Endpoints.HTTP != nil && len(c.Endpoints.HTTP.Responses) > 0 {
		// Sort by int key for deterministic violation ordering across runs.
		keys := make([]int, 0, len(c.Endpoints.HTTP.Responses))
		for k := range c.Endpoints.HTTP.Responses {
			keys = append(keys, k)
		}
		sort.Ints(keys)
		for _, k := range keys {
			r := c.Endpoints.HTTP.Responses[k]
			if r.SchemaRef != "" {
				paths = append(paths, filepath.Join(c.Dir, r.SchemaRef))
			}
		}
	}
	return paths
}

// scanSchemaForStrictMissing reads a JSON schema file and recursively walks it.
// For every object node whose "type" equals "object", it checks whether
// "additionalProperties" is set to false. It collects JSON-pointer-style paths
// of violations (e.g. "$", "$.data", "$.data.items").
func scanSchemaForStrictMissing(absPath string) ([]string, error) {
	raw, err := os.ReadFile(absPath)
	if err != nil {
		return nil, err
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, fmt.Errorf("invalid JSON schema %s: %w", absPath, err)
	}
	var missing []string
	walkSchemaObject(schema, "$", &missing)
	return missing, nil
}

// walkSchemaObject recursively walks a schema node. When the node is an object
// type (map with "type":"object"), it checks for additionalProperties:false and
// recurses into "properties" and "items".
func walkSchemaObject(node map[string]any, path string, missing *[]string) {
	walkSchemaObjectDepth(node, path, missing, 0)
}

// walkSchemaObjectDepth is the depth-guarded implementation of walkSchemaObject.
// depth > 32 causes early return to prevent unbounded recursion on pathological schemas.
//
// For "type":"object" nodes: checks additionalProperties and recurses into properties.
// For any node with an "items" sub-schema (including "type":"array" nodes): recurses
// into items so that array-typed properties with object items are also validated.
func walkSchemaObjectDepth(node map[string]any, path string, missing *[]string, depth int) {
	if depth > 32 {
		return
	}
	typVal, _ := node["type"].(string)
	if typVal == "object" {
		checkAdditionalProperties(node, path, missing)
		walkSchemaPropertiesDepth(node, path, missing, depth+1)
	}
	// Always recurse into items (covers "type":"array" nodes whose items is an object).
	walkSchemaItemsDepth(node, path, missing, depth+1)
}

// checkAdditionalProperties emits a violation when the node lacks exactly
// "additionalProperties": false. An object value (e.g. {"type":"string"}) is
// treated as missing — only bool(false) is accepted.
func checkAdditionalProperties(node map[string]any, path string, missing *[]string) {
	ap, hasAP := node["additionalProperties"]
	if !hasAP {
		*missing = append(*missing, path)
		return
	}
	b, ok := ap.(bool)
	if !ok || b {
		*missing = append(*missing, path)
	}
}

// walkSchemaPropertiesDepth is the depth-guarded implementation for recursing into properties.
func walkSchemaPropertiesDepth(node map[string]any, path string, missing *[]string, depth int) {
	props, ok := node["properties"].(map[string]any)
	if !ok {
		return
	}
	for key, val := range props {
		if child, ok := val.(map[string]any); ok {
			walkSchemaObjectDepth(child, path+"."+key, missing, depth)
		}
	}
}

// walkSchemaItemsDepth is the depth-guarded implementation for recursing into items.
func walkSchemaItemsDepth(node map[string]any, path string, missing *[]string, depth int) {
	items, ok := node["items"].(map[string]any)
	if ok {
		walkSchemaObjectDepth(items, path+".items", missing, depth)
	}
}

// --- FMT-21 (formerly FMT-CONTRACT-DIR-ID-MATCH-01) ---

// validateFMTContractDirIDMatch01 checks that every contract's Dir matches the
// directory derived from its ID. For example, "http.auth.login.v1" must live at
// "contracts/http/auth/login/v1".
//
// Contracts under example projects (e.g. "examples/iotdevice/contracts/…") are
// accepted as long as the segment after the last "contracts/" separator matches
// the ID-derived suffix. A Dir that contains no "contracts/" component at all
// is itself a violation.
//
// Severity: Error, IssueMismatch.
func (v *Validator) validateFMTContractDirIDMatch01() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if c.Dir == "" {
			continue
		}
		derived := filepath.Clean(contractDirFromID(c.ID)) // e.g. "contracts/http/auth/login/v1"
		actualClean := filepath.Clean(c.Dir)

		// Find the last path segment equal to "contracts" so that paths like
		// "examples/iotdevice/contracts/http/foo/v1" match the same derived suffix
		// as a top-level "contracts/http/foo/v1". Use segment-aware matching to
		// avoid matching "mycontracts/" as if it were a "contracts/" root.
		parts := strings.Split(filepath.ToSlash(actualClean), "/")
		lastIdx := -1
		for i, p := range parts {
			if p == "contracts" {
				lastIdx = i
			}
		}
		if lastIdx < 0 {
			// No "contracts" segment anywhere → definite mismatch.
			results = append(results, v.newResult(
				ruleFMT21, SeverityError, IssueMismatch,
				contractFile(c), "id",
				fmt.Sprintf("contract %q dir %q does not match derived %q", c.ID, c.Dir, derived),
			))
			continue
		}
		actualSuffix := filepath.Join(parts[lastIdx:]...) // "contracts/http/auth/login/v1"
		if actualSuffix != derived {
			results = append(results, v.newResult(
				ruleFMT21, SeverityError, IssueMismatch,
				contractFile(c), "id",
				fmt.Sprintf("contract %q dir %q does not match derived %q", c.ID, c.Dir, derived),
			))
		}
	}
	return results
}

// --- FMT-22 (formerly STATUSBOARD-STATE-ENUM-01) ---

// validStatusBoardStates contains the accepted state values for status-board entries.
var validStatusBoardStates = map[string]bool{
	"todo":  true,
	"doing": true,
	"done":  true,
}

// validateStatusBoardStateEnum01 checks that every status-board entry has a
// state from the allowed set {todo, doing, done}.
//
// Severity: Error, IssueInvalid.
func (v *Validator) validateStatusBoardStateEnum01() []ValidationResult {
	var results []ValidationResult
	for i, e := range v.project.StatusBoard {
		if !validStatusBoardStates[e.State] {
			results = append(results, v.newResult(
				ruleFMT22, SeverityError, IssueInvalid,
				"journeys/status-board.yaml",
				fmt.Sprintf("[%d].state", i),
				fmt.Sprintf(
					"status-board entry %q state %q must be one of {todo, doing, done}",
					e.JourneyID, e.State,
				),
			))
		}
	}
	return results
}

// --- FMT-23 (formerly CONTRACT-DEPRECATED-CLEANUP-01) ---

// validateContractDeprecatedCleanup01 enforces that deprecated contracts carry a
// valid deprecatedAt date and are not stale (>90 days since deprecation).
//
// Three cases:
//   - deprecated + empty deprecatedAt → Error, IssueRequired
//   - deprecated + malformed date → Error, IssueInvalid
//   - deprecated + date >90d ago → Warning, IssueForbidden
func (v *Validator) validateContractDeprecatedCleanup01() []ValidationResult {
	var results []ValidationResult
	now := v.now()
	for _, c := range v.project.Contracts {
		if c.Lifecycle != "deprecated" {
			continue
		}
		if c.DeprecatedAt == "" {
			results = append(results, v.newResult(
				ruleFMT23, SeverityError, IssueRequired,
				contractFile(c), "deprecatedAt",
				fmt.Sprintf("contract %q is deprecated but missing deprecatedAt", c.ID),
			))
			continue
		}
		ts, err := time.ParseInLocation("2006-01-02", c.DeprecatedAt, time.UTC)
		if err != nil {
			results = append(results, v.newResult(
				ruleFMT23, SeverityError, IssueInvalid,
				contractFile(c), "deprecatedAt",
				fmt.Sprintf("contract %q deprecatedAt %q is not YYYY-MM-DD", c.ID, c.DeprecatedAt),
			))
			continue
		}
		if now.UTC().Sub(ts) > 90*24*time.Hour {
			results = append(results, v.newResult(
				ruleFMT23, SeverityWarning, IssueForbidden,
				contractFile(c), "lifecycle",
				fmt.Sprintf(
					"contract %q has been deprecated for >90d (since %s); remove or extend",
					c.ID, c.DeprecatedAt,
				),
			))
		}
	}
	return results
}
