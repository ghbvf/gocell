package governance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// --- FMT-RESPONSE-STRICT-01 ---

// validateFMTResponseStrict01 scans every HTTP-kind contract's request/response
// JSON schemas. For each "type":"object" node in the schema (recursive), if it
// lacks "additionalProperties": false, a violation is emitted.
//
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
				// Missing schema file is reported by FMT-09 / REF rules; skip here.
				continue
			}
			for _, loc := range missing {
				results = append(results, v.newResult(
					"FMT-RESPONSE-STRICT-01", SeverityError, IssueRequired,
					schemaRef, loc,
					fmt.Sprintf("contract %q schema must declare additionalProperties:false at %s", c.ID, loc),
				))
			}
		}
	}
	return results
}

// collectHTTPSchemaPaths returns the relative schema paths (request + response)
// for an HTTP contract, resolved relative to the project root.
func collectHTTPSchemaPaths(c *metadata.ContractMeta) []string {
	var paths []string
	if c.SchemaRefs.Request != "" {
		paths = append(paths, filepath.Join(c.Dir, c.SchemaRefs.Request))
	}
	if c.SchemaRefs.Response != "" {
		paths = append(paths, filepath.Join(c.Dir, c.SchemaRefs.Response))
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
	typVal, _ := node["type"].(string)
	if typVal == "object" {
		checkAdditionalProperties(node, path, missing)
		walkSchemaProperties(node, path, missing)
	}
	walkSchemaItems(node, path, missing)
}

// checkAdditionalProperties emits a violation when the node lacks
// "additionalProperties": false.
func checkAdditionalProperties(node map[string]any, path string, missing *[]string) {
	ap, hasAP := node["additionalProperties"]
	if !hasAP || ap != false {
		*missing = append(*missing, path)
	}
}

// walkSchemaProperties recurses into the "properties" sub-map of a schema object node.
func walkSchemaProperties(node map[string]any, path string, missing *[]string) {
	props, ok := node["properties"].(map[string]any)
	if !ok {
		return
	}
	for key, val := range props {
		if child, ok := val.(map[string]any); ok {
			walkSchemaObject(child, path+"."+key, missing)
		}
	}
}

// walkSchemaItems recurses into the "items" sub-schema when it describes an object.
func walkSchemaItems(node map[string]any, path string, missing *[]string) {
	items, ok := node["items"].(map[string]any)
	if ok {
		walkSchemaObject(items, path+".items", missing)
	}
}

// --- FMT-CONTRACT-DIR-ID-MATCH-01 ---

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
	contractsSep := "contracts" + string(filepath.Separator)
	for _, c := range v.project.Contracts {
		if c.Dir == "" {
			continue
		}
		derived := filepath.Clean(contractDirFromID(c.ID)) // e.g. "contracts/http/auth/login/v1"
		actualClean := filepath.Clean(c.Dir)

		// Find the last occurrence of "contracts/" within the actual dir so that
		// paths like "examples/iotdevice/contracts/http/foo/v1" match the same
		// derived suffix as a top-level "contracts/http/foo/v1".
		idx := strings.LastIndex(actualClean, contractsSep)
		if idx < 0 {
			// No "contracts/" segment anywhere → definite mismatch.
			results = append(results, v.newResult(
				"FMT-CONTRACT-DIR-ID-MATCH-01", SeverityError, IssueMismatch,
				contractFile(c), "id",
				fmt.Sprintf("contract %q dir %q does not match derived %q", c.ID, c.Dir, derived),
			))
			continue
		}
		actualSuffix := actualClean[idx:] // "contracts/http/auth/login/v1"
		if actualSuffix != derived {
			results = append(results, v.newResult(
				"FMT-CONTRACT-DIR-ID-MATCH-01", SeverityError, IssueMismatch,
				contractFile(c), "id",
				fmt.Sprintf("contract %q dir %q does not match derived %q", c.ID, c.Dir, derived),
			))
		}
	}
	return results
}

// --- STATUSBOARD-STATE-ENUM-01 ---

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
				"STATUSBOARD-STATE-ENUM-01", SeverityError, IssueInvalid,
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

// --- CONTRACT-DEPRECATED-CLEANUP-01 ---

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
				"CONTRACT-DEPRECATED-CLEANUP-01", SeverityError, IssueRequired,
				contractFile(c), "deprecatedAt",
				fmt.Sprintf("contract %q is deprecated but missing deprecatedAt", c.ID),
			))
			continue
		}
		ts, err := time.Parse("2006-01-02", c.DeprecatedAt)
		if err != nil {
			results = append(results, v.newResult(
				"CONTRACT-DEPRECATED-CLEANUP-01", SeverityError, IssueInvalid,
				contractFile(c), "deprecatedAt",
				fmt.Sprintf("contract %q deprecatedAt %q is not YYYY-MM-DD", c.ID, c.DeprecatedAt),
			))
			continue
		}
		if now.Sub(ts) > 90*24*time.Hour {
			results = append(results, v.newResult(
				"CONTRACT-DEPRECATED-CLEANUP-01", SeverityWarning, IssueForbidden,
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
