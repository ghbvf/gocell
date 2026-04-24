package governance

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/pkg/contracts"
)

// pathPlaceholderRe extracts every `{name}` placeholder from an HTTP path
// template. Names follow Go identifier rules — ASCII letters, digits, and
// underscore. GoCell paths follow no-dash camelCase by convention; exotic
// chi/gorilla syntaxes (`{name-with-dash}`, `{name:regex}`, `{*wildcard}`)
// are out of scope. If such a template ever ships, FMT-13 will silently
// ignore the placeholder, and the downstream declaration-vs-template check
// will surface the drift as a "pathParams declared but not in template"
// error, so misuse fails loudly one way or another.
//
// ref: goadesign/goa v3 expr/http_endpoint.go HTTPWildcardRegex (similar
// ASCII-identifier scope).
var pathPlaceholderRe = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Package-level lookup maps for validation rules, avoiding per-call allocation.
var (
	validLifecycles = map[string]bool{
		string(cell.LifecycleDraft):      true,
		string(cell.LifecycleActive):     true,
		string(cell.LifecycleDeprecated): true,
	}
	validCellTypes = map[string]bool{
		string(cell.CellTypeCore):    true,
		string(cell.CellTypeEdge):    true,
		string(cell.CellTypeSupport): true,
	}
	validRoles = map[string]bool{
		string(cell.RoleServe):     true,
		string(cell.RoleCall):      true,
		string(cell.RolePublish):   true,
		string(cell.RoleSubscribe): true,
		string(cell.RoleHandle):    true,
		string(cell.RoleInvoke):    true,
		string(cell.RoleProvide):   true,
		string(cell.RoleRead):      true,
	}
	validKinds = map[string]bool{
		string(cell.ContractHTTP):       true,
		string(cell.ContractEvent):      true,
		string(cell.ContractCommand):    true,
		string(cell.ContractProjection): true,
	}
	validHTTPMethods = map[string]bool{
		"GET":    true,
		"POST":   true,
		"PUT":    true,
		"PATCH":  true,
		"DELETE": true,
	}
)

// validateFMT01 checks that contract.lifecycle is one of {draft, active, deprecated}.
func (v *Validator) validateFMT01() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if !validLifecycles[c.Lifecycle] {
			results = append(results, v.newResult(
				"FMT-01", SeverityError, IssueInvalid,
				contractFile(c.ID),
				"lifecycle",
				fmt.Sprintf("contract %q lifecycle %q is not valid (must be draft, active, or deprecated)", c.ID, c.Lifecycle),
			))
		}
	}
	return results
}

// validateFMT02 checks that cell.type is one of {core, edge, support}.
func (v *Validator) validateFMT02() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Cells {
		if !validCellTypes[c.Type] {
			results = append(results, v.newResult(
				"FMT-02", SeverityError, IssueInvalid,
				cellFile(c.ID),
				"type",
				fmt.Sprintf("cell %q type %q is not valid (must be core, edge, or support)", c.ID, c.Type),
			))
		}
	}
	return results
}

// validateFMT03 checks that consistencyLevel is valid (L0-L4) for both cells and contracts.
func (v *Validator) validateFMT03() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Cells {
		if _, err := cell.ParseLevel(c.ConsistencyLevel); err != nil {
			results = append(results, v.newResult(
				"FMT-03", SeverityError, IssueInvalid,
				cellFile(c.ID),
				"consistencyLevel",
				fmt.Sprintf("cell %q consistencyLevel %q is not valid (must be L0-L4)", c.ID, c.ConsistencyLevel),
			))
		}
	}
	for _, c := range v.project.Contracts {
		if _, err := cell.ParseLevel(c.ConsistencyLevel); err != nil {
			results = append(results, v.newResult(
				"FMT-03", SeverityError, IssueInvalid,
				contractFile(c.ID),
				"consistencyLevel",
				fmt.Sprintf("contract %q consistencyLevel %q is not valid (must be L0-L4)", c.ID, c.ConsistencyLevel),
			))
		}
	}
	return results
}

// validateFMT04 checks that event-type contracts include replayable, idempotencyKey, deliverySemantics,
// and that projection-type contracts include replayable.
func (v *Validator) validateFMT04() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		kind := cell.ContractKind(c.Kind)

		// Both event and projection contracts require replayable.
		if kind == cell.ContractEvent || kind == cell.ContractProjection {
			if c.Replayable == nil {
				results = append(results, v.newResult(
					"FMT-04", SeverityError, IssueRequired,
					contractFile(c.ID),
					"replayable",
					fmt.Sprintf("%s contract %q must specify replayable", c.Kind, c.ID),
				))
			}
		}

		// Only event contracts require idempotencyKey and deliverySemantics.
		if kind == cell.ContractEvent {
			if c.IdempotencyKey == "" {
				results = append(results, v.newResult(
					"FMT-04", SeverityError, IssueRequired,
					contractFile(c.ID),
					"idempotencyKey",
					fmt.Sprintf("event contract %q must specify idempotencyKey", c.ID),
				))
			}
			if c.DeliverySemantics == "" {
				results = append(results, v.newResult(
					"FMT-04", SeverityError, IssueRequired,
					contractFile(c.ID),
					"deliverySemantics",
					fmt.Sprintf("event contract %q must specify deliverySemantics", c.ID),
				))
			}
		}
	}
	return results
}

// validateFMT05 checks that contractUsages[].role is one of the 8 valid roles.
func (v *Validator) validateFMT05() []ValidationResult {
	var results []ValidationResult
	for key, s := range v.project.Slices {
		for i, cu := range s.ContractUsages {
			if !validRoles[cu.Role] {
				results = append(results, v.newResult(
					"FMT-05", SeverityError, IssueInvalid,
					sliceFile(key),
					fmt.Sprintf("contractUsages[%d].role", i),
					fmt.Sprintf("role %q is not a valid contract role", cu.Role),
				))
			}
		}
	}
	return results
}

// validateFMT06 checks that non-L0 cells must have schema.primary.
func (v *Validator) validateFMT06() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Cells {
		level, err := cell.ParseLevel(c.ConsistencyLevel)
		if err != nil {
			continue // FMT-03 covers invalid levels
		}
		if level != cell.L0 && c.Schema.Primary == "" {
			results = append(results, v.newResult(
				"FMT-06", SeverityError, IssueRequired,
				cellFile(c.ID),
				"schema.primary",
				fmt.Sprintf("non-L0 cell %q must have schema.primary", c.ID),
			))
		}
	}
	return results
}

// validateFMT07 checks that the contract provider endpoint is populated based on kind.
func (v *Validator) validateFMT07() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		provider := contractProvider(c)
		if provider == "" {
			var field string
			switch cell.ContractKind(c.Kind) {
			case cell.ContractHTTP:
				field = "endpoints.server"
			case cell.ContractEvent:
				field = "endpoints.publisher"
			case cell.ContractCommand:
				field = "endpoints.handler"
			case cell.ContractProjection:
				field = "endpoints.provider"
			default:
				field = "endpoints"
			}
			results = append(results, v.newResult(
				"FMT-07", SeverityError, IssueRequired,
				contractFile(c.ID),
				field,
				fmt.Sprintf("contract %q (kind %q) must have a provider endpoint", c.ID, c.Kind),
			))
		}
	}
	return results
}

// validateFMT09 checks that contract.kind is one of {http, event, command, projection}.
func (v *Validator) validateFMT09() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if !validKinds[c.Kind] {
			results = append(results, v.newResult(
				"FMT-09", SeverityError, IssueInvalid,
				contractFile(c.ID),
				"kind",
				fmt.Sprintf("contract %q kind %q is not valid (must be http, event, command, or projection)", c.ID, c.Kind),
			))
		}
	}
	return results
}

// bannedFieldNames are legacy camelCase field names that are no longer allowed
// in metadata YAML files (see metadata-model-v3.md migration appendix).
var bannedFieldNames = map[string]string{
	"cellId":            "id",
	"sliceId":           "id",
	"contractId":        "id",
	"assemblyId":        "id",
	"ownedSlices":       "(removed — generated by tooling)",
	"authoritativeData": "schema.primary",
	"producer":          "endpoints.publisher / endpoints.server",
	"consumers":         "endpoints.subscribers / endpoints.clients",
	"callsContracts":    "contractUsages",
	"publishes":         "contractUsages with role publish",
	"consumes":          "contractUsages with role subscribe",
}

// validateFMT10 checks that no metadata entity uses banned legacy field names
// as its ID. This is a heuristic check — it flags cells, slices, contracts,
// journeys, and assemblies whose ID exactly matches a banned field name.
// Full YAML-level field detection requires the parser to surface raw keys;
// this rule catches the most common mis-use patterns.
func (v *Validator) validateFMT10() []ValidationResult {
	var results []ValidationResult

	// Check cell IDs.
	for _, c := range v.project.Cells {
		if replacement, ok := bannedFieldNames[c.ID]; ok {
			results = append(results, v.newResult(
				"FMT-10", SeverityError, IssueForbidden,
				cellFile(c.ID),
				"id",
				fmt.Sprintf("cell ID %q is a banned legacy field name; use %q instead", c.ID, replacement),
			))
		}
	}

	// Check contract IDs for slash-separated format (should be dot-separated).
	for _, c := range v.project.Contracts {
		if strings.Contains(c.ID, "/") {
			results = append(results, v.newResult(
				"FMT-10", SeverityError, IssueInvalid,
				contractFile(c.ID),
				"id",
				fmt.Sprintf("contract ID %q uses slash separator; must use dot-separated format (e.g., kind.domain.version)", c.ID),
			))
		}
	}

	return results
}

// validateFMT08 checks that the first segment of a contract ID matches the contract's kind.
// Contract ID format: "{kind}.{domain}.{version}"; the prefix before the first "." should equal kind.
func (v *Validator) validateFMT08() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		parts := strings.SplitN(c.ID, ".", 2)
		if len(parts) < 2 {
			results = append(results, v.newResult(
				"FMT-08", SeverityError, IssueInvalid,
				contractFile(c.ID),
				"id",
				fmt.Sprintf("contract ID %q format is invalid (missing '.' separator)", c.ID),
			))
			continue
		}
		prefix := parts[0]
		if prefix != c.Kind {
			results = append(results, v.newResult(
				"FMT-08", SeverityError, IssueMismatch,
				contractFile(c.ID),
				"kind",
				fmt.Sprintf("contract %q ID prefix %q does not match kind %q", c.ID, prefix, c.Kind),
			))
		}
	}
	return results
}

// validateFMT11 checks that every cell has required owner and verify fields:
// owner.team, owner.role, and verify.smoke must be non-empty.
// CLAUDE.md mandates: cell.yaml must have owner{team,role} and verify.smoke.
func (v *Validator) validateFMT11() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Cells {
		if c.Owner.Team == "" {
			results = append(results, v.newResult(
				"FMT-11", SeverityError, IssueRequired,
				cellFile(c.ID),
				"owner.team",
				fmt.Sprintf("cell %q must have owner.team", c.ID),
			))
		}
		if c.Owner.Role == "" {
			results = append(results, v.newResult(
				"FMT-11", SeverityError, IssueRequired,
				cellFile(c.ID),
				"owner.role",
				fmt.Sprintf("cell %q must have owner.role", c.ID),
			))
		}
		if len(c.Verify.Smoke) == 0 {
			results = append(results, v.newResult(
				"FMT-11", SeverityError, IssueRequired,
				cellFile(c.ID),
				"verify.smoke",
				fmt.Sprintf("cell %q must have at least one verify.smoke entry", c.ID),
			))
		}
	}
	return results
}

// validateFMT12 checks that every slice has at least one verify.unit entry.
// CLAUDE.md mandates: slice.yaml must have verify.unit.
func (v *Validator) validateFMT12() []ValidationResult {
	var results []ValidationResult
	for key, s := range v.project.Slices {
		if len(s.Verify.Unit) == 0 {
			results = append(results, v.newResult(
				"FMT-12", SeverityError, IssueRequired,
				sliceFile(key),
				"verify.unit",
				fmt.Sprintf("slice %q must have at least one verify.unit entry", s.ID),
			))
		}
	}
	return results
}

const (
	// codeFMT13 is the rule code for HTTP transport metadata validation.
	codeFMT13 = "FMT-13"
	// fieldSchemaRefsResponse is the shared field path for response schema findings.
	fieldSchemaRefsResponse = "schemaRefs.response"
)

// validateFMT13 checks optional HTTP transport metadata on migrated HTTP contracts.
// Legacy HTTP contracts may omit endpoints.http entirely, but once present it must
// be internally consistent and must not conflict with no-content semantics.
func (v *Validator) validateFMT13() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if c.Endpoints.HTTP == nil {
			continue
		}
		results = append(results, v.validateFMT13ForContract(c)...)
	}
	return results
}

// validateFMT13ForContract validates a single contract's HTTP transport metadata.
func (v *Validator) validateFMT13ForContract(c *metadata.ContractMeta) []ValidationResult {
	httpMeta := c.Endpoints.HTTP
	file := contractFile(c.ID)

	if cell.ContractKind(c.Kind) != cell.ContractHTTP {
		return []ValidationResult{v.newResult(
			codeFMT13, SeverityError, IssueInvalid,
			file,
			"endpoints.http",
			fmt.Sprintf("contract %q can only declare endpoints.http when kind is http", c.ID),
		)}
	}

	var results []ValidationResult
	results = append(results, v.validateFMT13Method(c, httpMeta, file)...)
	pathResults := v.validateFMT13Path(c, httpMeta, file)
	results = append(results, pathResults...)
	results = append(results, v.validateFMT13Status(c, httpMeta, file)...)
	results = append(results, v.validateFMT13NoContent(c, httpMeta, file)...)
	// Skip pathParams reconciliation when path is empty/malformed — running it
	// would flood the report with phantom "declaration without placeholder"
	// errors that mislead the author away from the real (missing path) cause.
	// `pathResults` is empty ⇔ `validateFMT13Path` accepted the path; the path
	// validator today only emits Error-severity results, so zero length is a
	// reliable accept signal. If ever a path advisory Warning is introduced,
	// switch this to a hasErrors(pathResults) check to preserve intent.
	// queryParams has no path dependency and is not short-circuited.
	if len(pathResults) == 0 {
		results = append(results, v.validateFMT13PathParams(c, httpMeta, file)...)
	}
	results = append(results, v.validateFMT13QueryParams(c, httpMeta, file)...)
	return results
}

// extractPathPlaceholders returns the ordered, unique set of `{name}` tokens
// found in path. Order follows first appearance to keep error messages stable.
func extractPathPlaceholders(path string) []string {
	matches := pathPlaceholderRe.FindAllStringSubmatch(path, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(matches))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		name := m[1]
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// sortedParamKeys returns the map keys in stable order for deterministic diagnostics.
func sortedParamKeys(m map[string]contracts.ParamSchema) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// validateFMT13PathParams enforces two-way consistency between the `{name}`
// placeholders in `endpoints.http.path` and the keys of `endpoints.http.pathParams`:
// each placeholder must have a typed declaration, and no declaration may name
// a placeholder missing from the path. Also validates per-entry `type` / `format`.
func (v *Validator) validateFMT13PathParams(c *metadata.ContractMeta, h *metadata.HTTPTransportMeta, file string) []ValidationResult {
	placeholders := extractPathPlaceholders(h.Path)
	declared := h.PathParams

	var results []ValidationResult

	// Placeholder without declaration → Error.
	for _, name := range placeholders {
		if _, ok := declared[name]; !ok {
			results = append(results, v.newResult(
				codeFMT13, SeverityError, IssueRequired,
				file,
				"endpoints.http.pathParams",
				fmt.Sprintf("http contract %q path placeholder %q has no pathParams declaration", c.ID, name),
			))
		}
	}

	// Declaration without matching placeholder → Error.
	if len(declared) > 0 {
		placeholderSet := make(map[string]bool, len(placeholders))
		for _, name := range placeholders {
			placeholderSet[name] = true
		}
		for _, name := range sortedParamKeys(declared) {
			if !placeholderSet[name] {
				results = append(results, v.newResult(
					codeFMT13, SeverityError, IssueInvalid,
					file,
					fmt.Sprintf("endpoints.http.pathParams.%s", name),
					fmt.Sprintf("http contract %q declares pathParams.%s but path %q has no such placeholder", c.ID, name, h.Path),
				))
			}
		}
	}

	// Per-entry schema (type whitelist, no Required on path params since they
	// are required by definition).
	for _, name := range sortedParamKeys(declared) {
		p := declared[name]
		results = append(results, v.validateFMT13ParamSchema(c, file, "pathParams", name, p, true)...)
	}

	return results
}

// validateFMT13QueryParams validates per-entry schema for every queryParams key.
// Query parameters have no path counterpart, so there is no two-way check —
// only type whitelisting and format sanity.
func (v *Validator) validateFMT13QueryParams(c *metadata.ContractMeta, h *metadata.HTTPTransportMeta, file string) []ValidationResult {
	var results []ValidationResult
	for _, name := range sortedParamKeys(h.QueryParams) {
		p := h.QueryParams[name]
		results = append(results, v.validateFMT13ParamSchema(c, file, "queryParams", name, p, false)...)
	}
	return results
}

// validateFMT13ParamSchema validates the `type` / `required` / `format` triplet
// of a single ParamSchema. `isPath` toggles path-specific rules: `required: false`
// on a path parameter is a contradiction (path placeholders are required by
// definition) and is rejected.
func (v *Validator) validateFMT13ParamSchema(c *metadata.ContractMeta, file, kind, name string, p contracts.ParamSchema, isPath bool) []ValidationResult {
	var results []ValidationResult
	fieldBase := fmt.Sprintf("endpoints.http.%s.%s", kind, name)

	if p.Type == "" {
		results = append(results, v.newResult(
			codeFMT13, SeverityError, IssueRequired,
			file,
			fieldBase+".type",
			fmt.Sprintf("http contract %q %s.%s must specify type", c.ID, kind, name),
		))
	} else if !contracts.ParamTypes[p.Type] {
		results = append(results, v.newResult(
			codeFMT13, SeverityError, IssueInvalid,
			file,
			fieldBase+".type",
			fmt.Sprintf("http contract %q %s.%s type %q is not supported", c.ID, kind, name, p.Type),
		))
	}

	if isPath && p.Required != nil && !*p.Required {
		results = append(results, v.newResult(
			codeFMT13, SeverityError, IssueMismatch,
			file,
			fieldBase+".required",
			fmt.Sprintf("http contract %q pathParams.%s cannot be optional; path placeholders are required by definition", c.ID, name),
		))
	}

	return results
}

func (v *Validator) validateFMT13Method(c *metadata.ContractMeta, h *metadata.HTTPTransportMeta, file string) []ValidationResult {
	if h.Method == "" {
		return []ValidationResult{v.newResult(
			codeFMT13, SeverityError, IssueRequired,
			file,
			"endpoints.http.method",
			fmt.Sprintf("http contract %q must specify endpoints.http.method once endpoints.http is present", c.ID),
		)}
	}
	if !validHTTPMethods[strings.ToUpper(h.Method)] {
		return []ValidationResult{v.newResult(
			codeFMT13, SeverityError, IssueInvalid,
			file,
			"endpoints.http.method",
			fmt.Sprintf("http contract %q method %q is not supported", c.ID, h.Method),
		)}
	}
	return nil
}

func (v *Validator) validateFMT13Path(c *metadata.ContractMeta, h *metadata.HTTPTransportMeta, file string) []ValidationResult {
	if h.Path == "" {
		return []ValidationResult{v.newResult(
			codeFMT13, SeverityError, IssueRequired,
			file,
			"endpoints.http.path",
			fmt.Sprintf("http contract %q must specify endpoints.http.path once endpoints.http is present", c.ID),
		)}
	}
	if !strings.HasPrefix(h.Path, "/") {
		return []ValidationResult{v.newResult(
			codeFMT13, SeverityError, IssueInvalid,
			file,
			"endpoints.http.path",
			fmt.Sprintf("http contract %q path %q must start with '/'", c.ID, h.Path),
		)}
	}
	return nil
}

func (v *Validator) validateFMT13Status(c *metadata.ContractMeta, h *metadata.HTTPTransportMeta, file string) []ValidationResult {
	if h.SuccessStatus == 0 {
		return []ValidationResult{v.newResult(
			codeFMT13, SeverityError, IssueRequired,
			file,
			"endpoints.http.successStatus",
			fmt.Sprintf("http contract %q must specify endpoints.http.successStatus once endpoints.http is present", c.ID),
		)}
	}
	if h.SuccessStatus < 200 || h.SuccessStatus > 299 {
		return []ValidationResult{v.newResult(
			codeFMT13, SeverityError, IssueInvalid,
			file,
			"endpoints.http.successStatus",
			fmt.Sprintf("http contract %q successStatus %d must be a 2xx code", c.ID, h.SuccessStatus),
		)}
	}
	return nil
}

func (v *Validator) validateFMT13NoContent(c *metadata.ContractMeta, h *metadata.HTTPTransportMeta, file string) []ValidationResult {
	var results []ValidationResult

	if h.NoContent {
		if h.SuccessStatus != 0 && h.SuccessStatus != 204 {
			results = append(results, v.newResult(
				codeFMT13, SeverityError, IssueMismatch,
				file,
				"endpoints.http.noContent",
				fmt.Sprintf("http contract %q with noContent=true must use successStatus 204", c.ID),
			))
		}
		if c.SchemaRefs.Response != "" {
			results = append(results, v.newResult(
				codeFMT13, SeverityError, IssueForbidden,
				file,
				fieldSchemaRefsResponse,
				fmt.Sprintf("http contract %q with noContent=true must not declare schemaRefs.response", c.ID),
			))
		}
	} else if h.SuccessStatus == 204 {
		results = append(results, v.newResult(
			codeFMT13, SeverityError, IssueMismatch,
			file,
			"endpoints.http.noContent",
			fmt.Sprintf("http contract %q with successStatus 204 must set noContent=true", c.ID),
		))
	}

	// Advisory: noContent=false without schemaRefs.response is likely incomplete.
	if !h.NoContent && c.SchemaRefs.Response == "" {
		results = append(results, v.newResult(
			codeFMT13, SeverityWarning, IssueRequired,
			file,
			fieldSchemaRefsResponse,
			fmt.Sprintf("http contract %q with noContent=false should declare schemaRefs.response", c.ID),
		))
	}

	return results
}

// validateFMT14 checks that every slice declares explicit allowedFiles.
func (v *Validator) validateFMT14() []ValidationResult {
	var results []ValidationResult
	for key, s := range v.project.Slices {
		if len(s.AllowedFiles) == 0 {
			results = append(results, v.newResult(
				"FMT-14", SeverityError, IssueRequired,
				sliceFile(key),
				"allowedFiles",
				fmt.Sprintf(
					"slice %q must declare explicit allowedFiles (e.g., [%q])",
					s.ID, allowedFilesExample(s),
				),
			))
		}
	}
	return results
}

func allowedFilesExample(s *metadata.SliceMeta) string {
	if s != nil && s.File != "" {
		dir := strings.TrimSuffix(strings.ReplaceAll(s.File, "\\", "/"), "slice.yaml")
		if dir != s.File {
			return dir + "**"
		}
	}
	if s == nil {
		return "cells/<cell>/slices/<slice>/**"
	}
	return fmt.Sprintf("cells/%s/slices/%s/**", s.BelongsToCell, s.ID)
}

// codeFMT15 is the rule code for HTTP list response schema validation.
const codeFMT15 = "FMT-15"

// validateFMT15 checks that HTTP list-style response schemas:
//   - include "hasMore" in required fields
//   - include "nextCursor" in required fields and declare it as a property
//
// A response is a "list" when properties.data.type is "array".
// Skipped when root is empty, for non-HTTP contracts, or when the schema file cannot be read.
func (v *Validator) validateFMT15() []ValidationResult {
	if v.root == "" {
		return nil
	}
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		results = append(results, v.checkFMT15Contract(c)...)
	}
	return results
}

// checkFMT15Contract validates a single contract's list response schema for FMT-15.
// Returns one result per violated constraint.
func (v *Validator) checkFMT15Contract(c *metadata.ContractMeta) []ValidationResult {
	if c.Kind != "http" || c.SchemaRefs.Response == "" {
		return nil
	}
	contractDir := filepath.Join(v.root, contractDirFromMeta(c))
	schemaPath := filepath.Join(contractDir, c.SchemaRefs.Response)
	if !IsWithinRoot(v.root, schemaPath) {
		return nil
	}
	data, err := v.readFile(schemaPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // REF-12 handles missing files
		}
		return []ValidationResult{v.newResult(
			codeFMT15, SeverityError, IssueInvalid,
			contractFile(c.ID), fieldSchemaRefsResponse,
			fmt.Sprintf("cannot read response schema for contract %q: %v", c.ID, err),
		)}
	}
	info, err := parseListSchemaInfo(data)
	if err != nil {
		return []ValidationResult{v.newResult(
			codeFMT15, SeverityError, IssueInvalid,
			contractFile(c.ID), fieldSchemaRefsResponse,
			fmt.Sprintf("response schema for contract %q is not valid JSON: %v", c.ID, err),
		)}
	}
	if hasCombinator(info) && looksLikeListSchema(info) {
		return []ValidationResult{v.newResult(
			codeFMT15, SeverityWarning, IssueInvalid,
			contractFile(c.ID), fieldSchemaRefsResponse,
			fmt.Sprintf("response schema for contract %q uses oneOf/anyOf/allOf: FMT-15 cannot verify list constraints; split into single-shape contracts", c.ID),
		)}
	}
	if !isListSchema(info) {
		return nil
	}
	var results []ValidationResult
	if !hasMoreInRequired(info) {
		results = append(results, v.newResult(
			codeFMT15, SeverityError, IssueRequired,
			contractFile(c.ID),
			fieldSchemaRefsResponse,
			fmt.Sprintf("list response schema for contract %q must include \"hasMore\" in required fields", c.ID),
		))
	}
	if !hasNextCursorProperty(info) {
		results = append(results, v.newResult(
			codeFMT15, SeverityError, IssueRequired,
			contractFile(c.ID),
			fieldSchemaRefsResponse,
			fmt.Sprintf("list response schema for contract %q must declare \"nextCursor\" property", c.ID),
		))
	}
	if !hasNextCursorInRequired(info) {
		results = append(results, v.newResult(
			codeFMT15, SeverityError, IssueRequired,
			contractFile(c.ID),
			fieldSchemaRefsResponse,
			fmt.Sprintf("list response schema for contract %q must include \"nextCursor\" in required fields", c.ID),
		))
	}
	return results
}

// responseSchemaInfo holds the subset of JSON Schema fields needed for list-lint checks.
type responseSchemaInfo struct {
	Properties struct {
		Data struct {
			Type string `json:"type"`
		} `json:"data"`
		// NextCursor is non-nil when the "nextCursor" property is declared in the schema.
		NextCursor *json.RawMessage `json:"nextCursor"`
		// HasMore is non-nil when the "hasMore" property is declared in the schema.
		HasMore *json.RawMessage `json:"hasMore"`
	} `json:"properties"`
	Required []string `json:"required"`
	// Combinator fields: non-nil when the schema uses oneOf/anyOf/allOf at the root level.
	OneOf *json.RawMessage `json:"oneOf"`
	AnyOf *json.RawMessage `json:"anyOf"`
	AllOf *json.RawMessage `json:"allOf"`
}

// parseListSchemaInfo unmarshals the minimal fields needed for list-lint checks.
// Returns an error if data is not valid JSON.
func parseListSchemaInfo(data []byte) (responseSchemaInfo, error) {
	var info responseSchemaInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return info, err
	}
	return info, nil
}

// isListSchema checks if a JSON schema has properties.data.type == "array".
func isListSchema(info responseSchemaInfo) bool {
	return info.Properties.Data.Type == "array"
}

// hasCombinator reports whether the schema uses oneOf/anyOf/allOf at the root level.
func hasCombinator(info responseSchemaInfo) bool {
	return info.OneOf != nil || info.AnyOf != nil || info.AllOf != nil
}

// looksLikeListSchema reports whether a schema appears list-related by checking
// whether "hasMore" or "nextCursor" are declared in the top-level properties.
// Used together with hasCombinator to avoid false positives on non-list schemas.
func looksLikeListSchema(info responseSchemaInfo) bool {
	return info.Properties.HasMore != nil || hasNextCursorProperty(info)
}

// hasMoreInRequired checks if "hasMore" is in the JSON schema required array.
func hasMoreInRequired(info responseSchemaInfo) bool {
	for _, r := range info.Required {
		if r == "hasMore" {
			return true
		}
	}
	return false
}

// hasNextCursorProperty checks if "nextCursor" is declared as a schema property.
// The field must be declared and required because PageResult always serializes
// nextCursor, using an empty string on the last page.
// A "nextCursor": null declaration is treated as absent (null is not a valid schema).
func hasNextCursorProperty(info responseSchemaInfo) bool {
	return info.Properties.NextCursor != nil && string(*info.Properties.NextCursor) != "null"
}

// hasNextCursorInRequired checks if "nextCursor" is in the JSON schema required array.
func hasNextCursorInRequired(info responseSchemaInfo) bool {
	for _, r := range info.Required {
		if r == "nextCursor" {
			return true
		}
	}
	return false
}
