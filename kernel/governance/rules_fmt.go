package governance

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/metadata"
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
		string(cellvocab.LifecycleDraft):      true,
		string(cellvocab.LifecycleActive):     true,
		string(cellvocab.LifecycleDeprecated): true,
	}
	validJourneyLifecycles = map[string]bool{
		"active":       true,
		"experimental": true,
	}
	validPassCriterionModes = map[string]bool{
		"auto":   true,
		"manual": true,
	}
	validCellTypes = map[string]bool{
		string(cellvocab.CellTypeCore):    true,
		string(cellvocab.CellTypeEdge):    true,
		string(cellvocab.CellTypeSupport): true,
	}
	validRoles = map[string]bool{
		string(cellvocab.RoleServe):     true,
		string(cellvocab.RoleCall):      true,
		string(cellvocab.RolePublish):   true,
		string(cellvocab.RoleSubscribe): true,
		string(cellvocab.RoleHandle):    true,
		string(cellvocab.RoleInvoke):    true,
		string(cellvocab.RoleProvide):   true,
		string(cellvocab.RoleRead):      true,
	}
	validKinds = map[string]bool{
		string(cellvocab.ContractHTTP):       true,
		string(cellvocab.ContractEvent):      true,
		string(cellvocab.ContractCommand):    true,
		string(cellvocab.ContractProjection): true,
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
				codeFMT01, SeverityError, IssueInvalid,
				contractFile(c),
				"lifecycle",
				fmt.Sprintf("contract %q lifecycle %q is not valid (must be draft, active, or deprecated);"+
					" fix: set lifecycle to draft, active, or deprecated", c.ID, c.Lifecycle),
			))
		}
	}
	return results
}

// validateFMT24 checks journey lifecycle and passCriteria structural validity.
func (v *Validator) validateFMT24() []ValidationResult {
	var results []ValidationResult
	for _, j := range v.project.Journeys {
		file := journeyFile(j)
		if !validJourneyLifecycles[j.Lifecycle] {
			if j.Lifecycle == "" {
				results = append(results, v.newResult(
					codeFMT24, SeverityError, IssueRequired,
					file,
					"lifecycle",
					fmt.Sprintf("journey %q lifecycle is required (must be active or experimental);"+
						" fix: add lifecycle: active or lifecycle: experimental to the journey", j.ID),
				))
			} else {
				results = append(results, v.newResult(
					codeFMT24, SeverityError, IssueInvalid,
					file,
					"lifecycle",
					fmt.Sprintf("journey %q lifecycle %q is not valid (must be active or experimental);"+
						" fix: set lifecycle to active or experimental", j.ID, j.Lifecycle),
				))
			}
		}

		for i, pc := range j.PassCriteria {
			results = append(results, v.validatePassCriterionFMT24(j, file, i, pc)...)
		}
	}
	return results
}

func (v *Validator) validatePassCriterionFMT24(
	j *metadata.JourneyMeta,
	file string,
	i int,
	pc metadata.PassCriterion,
) []ValidationResult {
	modeField := fmt.Sprintf("passCriteria[%d].mode", i)
	if !validPassCriterionModes[pc.Mode] {
		return []ValidationResult{v.newResult(
			codeFMT24, SeverityError, IssueInvalid,
			file,
			modeField,
			fmt.Sprintf("journey %q passCriteria[%d].mode %q is not valid (must be auto or manual);"+
				" fix: set mode to auto or manual", j.ID, i, pc.Mode),
		)}
	}
	if pc.Mode == "auto" && strings.TrimSpace(pc.CheckRef) == "" {
		return []ValidationResult{v.newResult(
			codeFMT24, SeverityError, IssueRequired,
			file,
			fmt.Sprintf(fieldCritCheckRefTmpl, i),
			fmt.Sprintf("journey %q auto passCriteria[%d] requires checkRef; fix: add a checkRef pointing to a test target", j.ID, i),
		)}
	}
	if pc.Mode == "manual" && strings.TrimSpace(pc.CheckRef) != "" {
		return []ValidationResult{v.newResult(
			codeFMT24, SeverityError, IssueForbidden,
			file,
			fmt.Sprintf(fieldCritCheckRefTmpl, i),
			fmt.Sprintf("journey %q manual passCriteria[%d] must not declare checkRef;"+
				" fix: remove checkRef from this manual passCriteria entry", j.ID, i),
		)}
	}
	return nil
}

// validateFMT02 checks that cell.type is one of {core, edge, support}.
func (v *Validator) validateFMT02() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Cells {
		if !validCellTypes[c.Type] {
			results = append(results, v.newResult(
				codeFMT02, SeverityError, IssueInvalid,
				cellFile(c),
				"type",
				fmt.Sprintf("cell %q type %q is not valid (must be core, edge, or support); fix: set type to core, edge, or support", c.ID, c.Type),
			))
		}
	}
	return results
}

// validateFMT03 checks that consistencyLevel is valid (L0-L4) for both cells and metadata.
func (v *Validator) validateFMT03() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Cells {
		if _, err := cellvocab.ParseLevel(c.ConsistencyLevel); err != nil {
			results = append(results, v.newResult(
				codeFMT03, SeverityError, IssueInvalid,
				cellFile(c),
				"consistencyLevel",
				fmt.Sprintf("cell %q consistencyLevel %q is not valid (must be L0-L4);"+
					" fix: set consistencyLevel to L0, L1, L2, L3, or L4", c.ID, c.ConsistencyLevel),
			))
		}
	}
	for _, c := range v.project.Contracts {
		if _, err := cellvocab.ParseLevel(c.ConsistencyLevel); err != nil {
			results = append(results, v.newResult(
				codeFMT03, SeverityError, IssueInvalid,
				contractFile(c),
				"consistencyLevel",
				fmt.Sprintf("contract %q consistencyLevel %q is not valid (must be L0-L4);"+
					" fix: set consistencyLevel to L0, L1, L2, L3, or L4", c.ID, c.ConsistencyLevel),
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
		kind := cellvocab.ContractKind(c.Kind)

		// Both event and projection contracts require replayable.
		if kind == cellvocab.ContractEvent || kind == cellvocab.ContractProjection {
			if c.Replayable == nil {
				results = append(results, v.newResult(
					codeFMT04, SeverityError, IssueRequired,
					contractFile(c),
					"replayable",
					fmt.Sprintf("%s contract %q must specify replayable; fix: add replayable: true or replayable: false to the contract", c.Kind, c.ID),
				))
			}
		}

		// Only event contracts require idempotencyKey and deliverySemantics.
		if kind == cellvocab.ContractEvent {
			if c.IdempotencyKey == "" {
				results = append(results, v.newResult(
					codeFMT04, SeverityError, IssueRequired,
					contractFile(c),
					"idempotencyKey",
					fmt.Sprintf("event contract %q must specify idempotencyKey; fix: add an idempotencyKey field to the event contract", c.ID),
				))
			}
			if c.DeliverySemantics == "" {
				results = append(results, v.newResult(
					codeFMT04, SeverityError, IssueRequired,
					contractFile(c),
					"deliverySemantics",
					fmt.Sprintf("event contract %q must specify deliverySemantics; fix: add deliverySemantics: at-least-once or at-most-once", c.ID),
				))
			}
		}
	}
	return results
}

// validateFMT05 checks that contractUsages[].role is one of the 8 valid roles.
func (v *Validator) validateFMT05() []ValidationResult {
	var results []ValidationResult
	for _, s := range v.project.Slices {
		for i, cu := range s.ContractUsages {
			if !validRoles[cu.Role] {
				results = append(results, v.newResult(
					codeFMT05, SeverityError, IssueInvalid,
					sliceFile(s),
					fmt.Sprintf("contractUsages[%d].role", i),
					fmt.Sprintf("role %q is not a valid contract role; fix: use one of the valid contract roles", cu.Role),
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
		level, err := cellvocab.ParseLevel(c.ConsistencyLevel)
		if err != nil {
			continue // FMT-03 covers invalid levels
		}
		if level != cellvocab.L0 && c.Schema.Primary == "" {
			results = append(results, v.newResult(
				codeFMT06, SeverityError, IssueRequired,
				cellFile(c),
				"schema.primary",
				fmt.Sprintf("non-L0 cell %q must have schema.primary; fix: add schema.primary pointing to the primary schema file", c.ID),
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
			switch cellvocab.ContractKind(c.Kind) {
			case cellvocab.ContractHTTP:
				field = "endpoints.server"
			case cellvocab.ContractEvent:
				field = "endpoints.publisher"
			case cellvocab.ContractCommand:
				field = "endpoints.handler"
			case cellvocab.ContractProjection:
				field = "endpoints.provider"
			default:
				field = "endpoints"
			}
			results = append(results, v.newResult(
				codeFMT07, SeverityError, IssueRequired,
				contractFile(c),
				field,
				fmt.Sprintf("contract %q (kind %q) must have a provider endpoint;"+
					" fix: add the required endpoint (server/publisher/handler/provider)", c.ID, c.Kind),
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
				codeFMT09, SeverityError, IssueInvalid,
				contractFile(c),
				"kind",
				fmt.Sprintf("contract %q kind %q is not valid (must be http, event, command, or projection);"+
					" fix: set kind to http, event, command, or projection", c.ID, c.Kind),
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
				codeFMT10, SeverityError, IssueForbidden,
				cellFile(c),
				"id",
				fmt.Sprintf("cell ID %q is a banned legacy field name; use %q instead;"+
					" fix: rename the cell to use the replacement field name", c.ID, replacement),
			))
		}
	}

	// Check contract IDs for slash-separated format (should be dot-separated).
	for _, c := range v.project.Contracts {
		if strings.Contains(c.ID, "/") {
			results = append(results, v.newResult(
				codeFMT10, SeverityError, IssueInvalid,
				contractFile(c),
				"id",
				fmt.Sprintf("contract ID %q uses slash separator; must use dot-separated format (e.g., kind.domain.version);"+
					" fix: rename the contract id to use dots as separators", c.ID),
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
				codeFMT08, SeverityError, IssueInvalid,
				contractFile(c),
				"id",
				fmt.Sprintf("contract ID %q format is invalid (missing '.' separator); fix: use format kind.domain.version", c.ID),
			))
			continue
		}
		prefix := parts[0]
		if prefix != c.Kind {
			results = append(results, v.newResult(
				codeFMT08, SeverityError, IssueMismatch,
				contractFile(c),
				"kind",
				fmt.Sprintf("contract %q ID prefix %q does not match kind %q;"+
					" fix: ensure the contract id starts with the contract kind", c.ID, prefix, c.Kind),
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
				codeFMT11, SeverityError, IssueRequired,
				cellFile(c),
				"owner.team",
				fmt.Sprintf("cell %q must have owner.team; fix: add owner.team to the cell.yaml", c.ID),
			))
		}
		if c.Owner.Role == "" {
			results = append(results, v.newResult(
				codeFMT11, SeverityError, IssueRequired,
				cellFile(c),
				"owner.role",
				fmt.Sprintf("cell %q must have owner.role; fix: add owner.role to the cell.yaml", c.ID),
			))
		}
		if len(c.Verify.Smoke) == 0 {
			results = append(results, v.newResult(
				codeFMT11, SeverityError, IssueRequired,
				cellFile(c),
				"verify.smoke",
				fmt.Sprintf("cell %q must have at least one verify.smoke entry; fix: add a verify.smoke entry pointing to a smoke test", c.ID),
			))
		}
	}
	return results
}

// validateFMT12 checks that every slice has at least one verify.unit entry.
// CLAUDE.md mandates: slice.yaml must have verify.unit.
func (v *Validator) validateFMT12() []ValidationResult {
	var results []ValidationResult
	for _, s := range v.project.Slices {
		if len(s.Verify.Unit) == 0 {
			results = append(results, v.newResult(
				codeFMT12, SeverityError, IssueRequired,
				sliceFile(s),
				"verify.unit",
				fmt.Sprintf("slice %q must have at least one verify.unit entry; fix: add a verify.unit entry pointing to a unit test", s.ID),
			))
		}
	}
	return results
}

const (
	// fieldSchemaRefsResponse is the shared field path for response schema findings.
	fieldSchemaRefsResponse = "schemaRefs.response"
)

// validateFMT13 checks HTTP transport metadata on metadata.
//
// Two cases are checked:
//   - kind=http with nil endpoints.http → Error: required block missing (FMT-13 必填化)
//   - any kind with non-nil endpoints.http → delegate to validateFMT13ForContract,
//     which rejects non-http contracts declaring endpoints.http and validates the
//     block's internal consistency for http metadata.
func (v *Validator) validateFMT13() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		isHTTP := cellvocab.ContractKind(c.Kind) == cellvocab.ContractHTTP
		if isHTTP && c.Endpoints.HTTP == nil {
			// FMT-13 必填化: HTTP contracts must now declare endpoints.http.
			results = append(results, v.newResult(
				codeFMT13, SeverityError, IssueRequired,
				contractFile(c),
				"endpoints.http",
				fmt.Sprintf(advHintFMT13MissingHTTP, c.ID),
			))
			continue
		}
		if c.Endpoints.HTTP == nil {
			// Non-HTTP contract without endpoints.http — nothing to validate.
			continue
		}
		// endpoints.http is non-nil: validate it (validateFMT13ForContract also
		// rejects non-http contracts that erroneously declare endpoints.http).
		results = append(results, v.validateFMT13ForContract(c)...)
	}
	return results
}

// validateFMT13ForContract validates a single contract's HTTP transport metadata.
func (v *Validator) validateFMT13ForContract(c *metadata.ContractMeta) []ValidationResult {
	httpMeta := c.Endpoints.HTTP
	file := contractFile(c)

	if cellvocab.ContractKind(c.Kind) != cellvocab.ContractHTTP {
		return []ValidationResult{v.newResult(
			codeFMT13, SeverityError, IssueInvalid,
			file,
			"endpoints.http",
			fmt.Sprintf("contract %q can only declare endpoints.http when kind is http;"+
				" fix: remove endpoints.http or change the contract kind to http", c.ID),
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
func sortedParamKeys(m map[string]metadata.ParamSchema) []string {
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
	// PR239-DX1: append a YAML fix hint so the diagnostic tells the user
	// *how* to fix it, not just *what* is missing. Indentation matches the
	// schema (top-level `pathParams:` under `endpoints.http:` block).
	for _, name := range placeholders {
		if _, ok := declared[name]; !ok {
			results = append(results, v.newResult(
				codeFMT13, SeverityError, IssueRequired,
				file,
				"endpoints.http.pathParams",
				fmt.Sprintf(advHintFMT13MissingPathParam, c.ID, name, name),
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
					fmt.Sprintf("http contract %q declares pathParams.%s but path %q has no such placeholder;"+
						" fix: remove the undeclared pathParam or add the placeholder to the path", c.ID, name, h.Path),
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
func (v *Validator) validateFMT13ParamSchema(
	c *metadata.ContractMeta, file, kind, name string,
	p metadata.ParamSchema, isPath bool,
) []ValidationResult {
	var results []ValidationResult
	fieldBase := fmt.Sprintf("endpoints.http.%s.%s", kind, name)

	if p.Type == "" {
		results = append(results, v.newResult(
			codeFMT13, SeverityError, IssueRequired,
			file,
			fieldBase+".type",
			fmt.Sprintf("http contract %q %s.%s must specify type; fix: add a type field (string, integer, boolean, number)", c.ID, kind, name),
		))
	} else if !metadata.ParamTypes[p.Type] {
		results = append(results, v.newResult(
			codeFMT13, SeverityError, IssueInvalid,
			file,
			fieldBase+".type",
			fmt.Sprintf("http contract %q %s.%s type %q is not supported;"+
				" fix: use one of string, integer, boolean, or number", c.ID, kind, name, p.Type),
		))
	}

	if isPath && p.Required != nil && !*p.Required {
		results = append(results, v.newResult(
			codeFMT13, SeverityError, IssueMismatch,
			file,
			fieldBase+".required",
			fmt.Sprintf("http contract %q pathParams.%s cannot be optional; path placeholders are required by definition;"+
				" fix: remove the required: false field or leave required unset", c.ID, name),
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
			fmt.Sprintf("http contract %q must specify endpoints.http.method once endpoints.http is present;"+
				" fix: add method: GET/POST/PUT/PATCH/DELETE", c.ID),
		)}
	}
	if !validHTTPMethods[strings.ToUpper(h.Method)] {
		return []ValidationResult{v.newResult(
			codeFMT13, SeverityError, IssueInvalid,
			file,
			"endpoints.http.method",
			fmt.Sprintf("http contract %q method %q is not supported; fix: use one of GET, POST, PUT, PATCH, DELETE", c.ID, h.Method),
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
			fmt.Sprintf("http contract %q must specify endpoints.http.path once endpoints.http is present; fix: add path starting with /", c.ID),
		)}
	}
	if !strings.HasPrefix(h.Path, "/") {
		return []ValidationResult{v.newResult(
			codeFMT13, SeverityError, IssueInvalid,
			file,
			"endpoints.http.path",
			fmt.Sprintf("http contract %q path %q must start with '/'; fix: ensure the path begins with /", c.ID, h.Path),
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
			fmt.Sprintf("http contract %q must specify endpoints.http.successStatus once endpoints.http is present;"+
				" fix: add successStatus: 200 or another 2xx code", c.ID),
		)}
	}
	if h.SuccessStatus < 200 || h.SuccessStatus > 299 {
		return []ValidationResult{v.newResult(
			codeFMT13, SeverityError, IssueInvalid,
			file,
			"endpoints.http.successStatus",
			fmt.Sprintf("http contract %q successStatus %d must be a 2xx code; fix: use a status code in the range 200-299", c.ID, h.SuccessStatus),
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
				fmt.Sprintf("http contract %q with noContent=true must use successStatus 204;"+
					" fix: change successStatus to 204 or remove noContent", c.ID),
			))
		}
		if c.SchemaRefs.Response != "" {
			results = append(results, v.newResult(
				codeFMT13, SeverityError, IssueForbidden,
				file,
				fieldSchemaRefsResponse,
				fmt.Sprintf("http contract %q with noContent=true must not declare schemaRefs.response;"+
					" fix: remove schemaRefs.response for no-content responses", c.ID),
			))
		}
	} else if h.SuccessStatus == 204 {
		results = append(results, v.newResult(
			codeFMT13, SeverityError, IssueMismatch,
			file,
			"endpoints.http.noContent",
			fmt.Sprintf("http contract %q with successStatus 204 must set noContent=true; fix: add noContent: true to the endpoint", c.ID),
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

// validateFMT26 checks that auth.public and auth.passwordResetExempt are not
// both true on the same HTTP endpoint. The two flags are semantically
// contradictory: public skips JWT entirely, while passwordResetExempt requires
// a valid JWT that carries password_reset_required. Declaring both is always a
// misconfiguration that the runtime would resolve ambiguously.
//
// The governance rule complements the JSON Schema `not.required` constraint in
// contract.schema.json — schema validation catches YAML-level structure while
// this rule fires on the parsed in-memory model, providing a clear error
// message with file+field attribution in the governance report.
//
// ref: JSON Schema 2020-12 §10.2.1 not
// ref: kubernetes/kubernetes validation-gen declarative + handwritten dual-layer pattern
func (v *Validator) validateFMT26() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if c.Endpoints.HTTP == nil {
			continue
		}
		auth := c.Endpoints.HTTP.Auth
		if auth.Public && auth.PasswordResetExempt {
			results = append(results, v.newResult(
				codeFMT26, SeverityError, IssueForbidden,
				contractFile(c),
				"endpoints.http.auth",
				fmt.Sprintf(
					"contract %q declares both auth.public and auth.passwordResetExempt; "+
						"they are mutually exclusive: public skips JWT entirely while "+
						"passwordResetExempt requires a valid JWT; fix: remove one of the conflicting auth flags",
					c.ID,
				),
			))
		}
	}
	return results
}

// validateFMT14 checks that every slice declares explicit allowedFiles.
func (v *Validator) validateFMT14() []ValidationResult {
	var results []ValidationResult
	for _, s := range v.project.Slices {
		if len(s.AllowedFiles) == 0 {
			results = append(results, v.newResult(
				codeFMT14, SeverityError, IssueRequired,
				sliceFile(s),
				"allowedFiles",
				fmt.Sprintf(
					"slice %q must declare explicit allowedFiles (e.g., [%q]); fix: add allowedFiles listing the files owned by this slice",
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
	resolved, resolveErr := metadata.ResolveContractSchemaRef(v.root, c, metadata.ContractSchemaRef{
		Field: fieldSchemaRefsResponse,
		Ref:   c.SchemaRefs.Response,
		Scope: metadata.SchemaRefScopeContractDir,
	})
	if resolveErr != nil {
		return []ValidationResult{v.newResult(
			codeFMT15, SeverityError, IssueInvalid,
			contractFile(c), fieldSchemaRefsResponse,
			fmt.Sprintf("cannot resolve response schema for contract %q: %v;"+
				" fix: ensure schemaRefs.response points to a valid schema file", c.ID, resolveErr),
		)}
	}
	data, err := v.readFile(resolved.AbsPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // REF-12 handles missing files
		}
		return []ValidationResult{v.newResult(
			codeFMT15, SeverityError, IssueInvalid,
			contractFile(c), fieldSchemaRefsResponse,
			fmt.Sprintf("cannot read response schema for contract %q: %v; fix: ensure the response schema file exists and is readable", c.ID, err),
		)}
	}
	info, err := parseListSchemaInfo(data)
	if err != nil {
		return []ValidationResult{v.newResult(
			codeFMT15, SeverityError, IssueInvalid,
			contractFile(c), fieldSchemaRefsResponse,
			fmt.Sprintf("response schema for contract %q is not valid JSON: %v; fix: fix the JSON syntax in the response schema file", c.ID, err),
		)}
	}
	if hasCombinator(info) && looksLikeListSchema(info) {
		return []ValidationResult{v.newResult(
			codeFMT15, SeverityWarning, IssueInvalid,
			contractFile(c), fieldSchemaRefsResponse,
			fmt.Sprintf("response schema for contract %q uses oneOf/anyOf/allOf:"+
				" FMT-15 cannot verify list constraints; split into single-shape contracts", c.ID),
		)}
	}
	if !isListSchema(info) {
		return nil
	}
	var results []ValidationResult
	if !hasMoreInRequired(info) {
		results = append(results, v.newResult(
			codeFMT15, SeverityError, IssueRequired,
			contractFile(c),
			fieldSchemaRefsResponse,
			fmt.Sprintf("list response schema for contract %q must include \"hasMore\" in required fields;"+
				" fix: add \"hasMore\" to the required array in the response schema", c.ID),
		))
	}
	if !hasNextCursorProperty(info) {
		results = append(results, v.newResult(
			codeFMT15, SeverityError, IssueRequired,
			contractFile(c),
			fieldSchemaRefsResponse,
			fmt.Sprintf("list response schema for contract %q must declare \"nextCursor\" property;"+
				" fix: add a \"nextCursor\" property to the response schema", c.ID),
		))
	}
	if !hasNextCursorInRequired(info) {
		results = append(results, v.newResult(
			codeFMT15, SeverityError, IssueRequired,
			contractFile(c),
			fieldSchemaRefsResponse,
			fmt.Sprintf("list response schema for contract %q must include \"nextCursor\" in required fields;"+
				" fix: add \"nextCursor\" to the required array in the response schema", c.ID),
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
	return slices.Contains(info.Required, "hasMore")
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
	return slices.Contains(info.Required, "nextCursor")
}

// validateFMT27 checks mutually exclusive HTTP auth metadata modes.
//
// These flags are semantically contradictory when combined:
//   - public skips JWT entirely (no authentication)
//   - bootstrap requires env-credential Basic Auth (dedicated first-admin gate)
//   - passwordResetExempt requires a valid JWT carrying password_reset_required
//   - clientsOnly relies on Contract.Clients caller-cell authorization
//   - serviceOwned relies on listener JWT auth plus service-layer ownership checks
//
// serviceOwned may combine with passwordResetExempt. All other combinations among
// public/bootstrap/passwordResetExempt/clientsOnly, and serviceOwned with
// public/bootstrap/clientsOnly, are rejected.
//
// ref: kubernetes/kubernetes validation-gen declarative + handwritten dual-layer pattern
func (v *Validator) validateFMT27() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if c.Endpoints.HTTP == nil {
			continue
		}
		auth := c.Endpoints.HTTP.Auth
		if !hasFMT27AuthModeConflict(auth) {
			continue
		}
		results = append(results, v.newResult(
			codeFMT27, SeverityError, IssueForbidden,
			contractFile(c),
			"endpoints.http.auth",
			fmt.Sprintf(
				"contract %q has incompatible auth mode combination: %s set to true. "+
					"Set at most one of {auth.public, auth.bootstrap, "+
					"auth.passwordResetExempt, auth.clientsOnly}; "+
					"only auth.serviceOwned may pair with auth.passwordResetExempt; fix: remove the conflicting auth mode flags",
				c.ID, formatTrueAuthFields(auth),
			),
		))
	}
	return results
}

// hasFMT27AuthModeConflict delegates to metadata.AuthComboLegal so the schema
// (contract.schema.json if/then) and governance share a single oracle. Adding
// a new auth bool field requires updating only AuthComboLegal +
// IterateAuthBoolCombos; no FMT-27 changes needed.
func hasFMT27AuthModeConflict(auth metadata.HTTPAuthMeta) bool {
	return !metadata.AuthComboLegal(auth)
}

// formatTrueAuthFields lists the auth bool fields currently set to true, in
// the canonical P-R-S-B-C order, so FMT-27 diagnostics pinpoint the offending
// flags rather than only naming the contract. Used by validateFMT27.
func formatTrueAuthFields(auth metadata.HTTPAuthMeta) string {
	var fields []string
	if auth.Public {
		fields = append(fields, "auth.public")
	}
	if auth.PasswordResetExempt {
		fields = append(fields, "auth.passwordResetExempt")
	}
	if auth.ServiceOwned {
		fields = append(fields, "auth.serviceOwned")
	}
	if auth.Bootstrap {
		fields = append(fields, "auth.bootstrap")
	}
	if auth.ClientsOnly {
		fields = append(fields, "auth.clientsOnly")
	}
	return strings.Join(fields, ", ")
}

// validateFMT30 enforces that every assembly's build.deployTemplate is one of
// metadata.DeployTemplateEnum (or empty, in which case parser derivation
// applies the default). The schema literal at
// schemas/assembly.schema.json deployTemplate.enum is kept byte-equal to
// metadata.DeployTemplateEnum by TestSchemaConstantsMatchSchemaLiterals;
// governance is the sole runtime gatekeeper that rejects out-of-enum values.
//
// Without this rule, schema-aware tooling rejects out-of-enum values but
// `gocell validate` accepts them, leaving CLI users with a different
// contract than the schema declares (see review §F2).
func (v *Validator) validateFMT30() []ValidationResult {
	var results []ValidationResult
	for _, asm := range v.project.Assemblies {
		if asm == nil {
			continue
		}
		dt := asm.Build.DeployTemplate
		if dt == "" || metadata.IsKnownDeployTemplate(dt) {
			continue
		}
		results = append(results, v.newResult(
			codeFMT30, SeverityError, IssueInvalid,
			assemblyFile(asm),
			"build.deployTemplate",
			fmt.Sprintf(
				"assembly %q build.deployTemplate=%q is not one of %v; fix: set build.deployTemplate to one of the allowed values",
				asm.ID, dt, metadata.DeployTemplateEnum,
			),
		))
	}
	return results
}

// validateFMT29 checks that every assembly declares a non-empty owner.team and
// owner.role. Assembly ownership complements the JSON Schema required constraint
// by providing a governance-layer finding with file+field attribution in the
// governance report. Mirrors the cell owner check in validateFMT11.
func (v *Validator) validateFMT29() []ValidationResult {
	var results []ValidationResult
	for _, asm := range v.project.Assemblies {
		if asm == nil {
			continue
		}
		if asm.Owner.Team == "" {
			results = append(results, v.newResult(
				codeFMT29, SeverityError, IssueRequired,
				assemblyFile(asm),
				"owner.team",
				fmt.Sprintf("assembly %q must have owner.team; fix: add owner.team to the assembly.yaml", asm.ID),
			))
		}
		if asm.Owner.Role == "" {
			results = append(results, v.newResult(
				codeFMT29, SeverityError, IssueRequired,
				assemblyFile(asm),
				"owner.role",
				fmt.Sprintf("assembly %q must have owner.role; fix: add owner.role to the assembly.yaml", asm.ID),
			))
		}
	}
	return results
}

// validateFMT28 checks auth mode placement/shape constraints that require fields
// outside the auth object itself.
//
//   - bootstrap is only allowed on paths matching metadata.IsBootstrapPath.
//   - clientsOnly is only allowed on metadata.IsInternalHTTPPath paths and must
//     declare endpoints.clients so RequireCallerCell has an allowlist.
func (v *Validator) validateFMT28() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if c.Endpoints.HTTP == nil {
			continue
		}
		path := c.Endpoints.HTTP.Path
		auth := c.Endpoints.HTTP.Auth
		if auth.Bootstrap && !metadata.IsBootstrapPath(path) {
			results = append(results, v.newResult(
				codeFMT28, SeverityError, IssueForbidden,
				contractFile(c),
				"endpoints.http.auth.bootstrap",
				fmt.Sprintf(
					"contract %q has auth.bootstrap:true on path %q; "+
						"bootstrap auth is only permitted on setup/admin contracts "+
						"(path must match IsBootstrapPath: /api/v{N}/{cell}/setup/admin); fix: use auth.bootstrap only on setup/admin paths",
					c.ID, path,
				),
			))
		}
		if !auth.ClientsOnly {
			continue
		}
		if !metadata.IsInternalHTTPPath(path) {
			results = append(results, v.newResult(
				codeFMT28, SeverityError, IssueForbidden,
				contractFile(c),
				"endpoints.http.auth.clientsOnly",
				fmt.Sprintf(
					"contract %q has auth.clientsOnly:true on path %q; "+
						"clientsOnly auth is only permitted on internal HTTP paths "+
						"(path must match IsInternalHTTPPath: /internal/v1 or /internal/v1/...);"+
						" fix: move the endpoint to an /internal/v1 path or remove auth.clientsOnly",
					c.ID, path,
				),
			))
		}
		if len(c.Endpoints.Clients) == 0 {
			results = append(results, v.newResult(
				codeFMT28, SeverityError, IssueRequired,
				contractFile(c),
				"endpoints.clients",
				fmt.Sprintf(
					"contract %q has auth.clientsOnly:true but endpoints.clients is empty; "+
						"clientsOnly auth requires at least one declared client cell; fix: add at least one cell id to endpoints.clients",
					c.ID,
				),
			))
		}
	}
	return results
}

// validateFMT31 enforces that every HTTP contract whose path matches
// metadata.IsInternalHTTPPath declares a non-empty endpoints.clients list.
//
// /internal/v1/* endpoints rely on Contract.Clients caller-cell allowlist via
// runtime auth.RequireCallerCell — an empty allowlist means anyone holding a
// valid service token can call, which defeats the purpose of internal-port
// isolation. This rule lifts the check from the L5 archtest
// (tools/archtest/contract_spec_clients_test.go) that scanned generated Go
// ContractSpec literals up to the L6 contract.yaml source of truth (charter
// §5.1 L5→L6 carrier migration); codegen at
// tools/codegen/contractgen/builder.go faithfully copies endpoints.clients to
// the Go literal, and runtime kernel/contractspec/spec.go::validateHTTP
// catches any drift at boot.
//
// FMT-31 is intentionally unidirectional. The inverse direction
// (non-internal path forbids non-empty clients) cannot be enforced here:
// endpoints.clients is semantically polymorphic — on internal paths it
// declares the caller-cell allowlist that codegen copies into
// ContractSpec.Clients; on non-internal paths it is declarative consumer
// metadata that codegen (tools/codegen/contractgen/builder.go) filters out
// of the runtime ContractSpec entirely (FMT-28 forbids auth.clientsOnly
// outside internal paths, so clientsOnly cannot pick up these declarations
// either). The runtime check at kernel/contractspec/spec.go remains the
// sole inverse-direction gate.
//
// ref: kernel/contractspec/spec.go::validateHTTP for the runtime mirror;
// ADR docs/architecture/202605051500-adr-k05-markergen-cellgen-unified.md
// for the deleted FMT-18 predecessor.
func (v *Validator) validateFMT31() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if c.Kind != "http" {
			continue
		}
		if c.Endpoints.HTTP == nil {
			continue
		}
		if !metadata.IsInternalHTTPPath(c.Endpoints.HTTP.Path) {
			continue
		}
		if len(c.Endpoints.Clients) > 0 {
			continue
		}
		results = append(results, v.newResult(
			codeFMT31, SeverityError, IssueRequired,
			contractFile(c),
			"endpoints.clients",
			fmt.Sprintf(
				"internal HTTP contract %q (path %q) has empty endpoints.clients; "+
					"/internal/v1/ contracts must declare at least one caller cell "+
					"so RequireCallerCell has an allowlist; fix: add caller cell ids to endpoints.clients",
				c.ID, c.Endpoints.HTTP.Path,
			),
		))
	}
	return results
}

// =============================================================================
// FMT-32 — serviceOwned contracts must declare endpoints.http.ownership
// =============================================================================

// validateFMT32 enforces OWNERSHIP-DECLARATION-REQUIRED-01: every HTTP contract
// with auth.serviceOwned=true must declare an endpoints.http.ownership block with
// valid subjectPath and resourcePath expressions conforming to the DSL defined by
// metadata.OwnershipPathValid, and path.<param>.* forms must reference a param
// declared in endpoints.http.pathParams.
//
// Governance oracle: metadata.OwnershipDeclarationRequired (single-source predicate
// shared with contract.schema.json if/then rule).
func (v *Validator) validateFMT32() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if c.Kind != "http" {
			continue
		}
		h := c.Endpoints.HTTP
		if h == nil {
			continue
		}
		if !metadata.OwnershipDeclarationRequired(h.Auth) {
			continue
		}
		if h.Ownership == nil {
			results = append(results, v.newResult(
				codeFMT32, SeverityError, IssueRequired,
				contractFile(c),
				"endpoints.http.ownership",
				fmt.Sprintf(
					"contract %q has auth.serviceOwned=true but no ownership block; "+
						"declare endpoints.http.ownership.subjectPath and resourcePath",
					c.ID,
				),
			))
			continue
		}
		results = append(results, v.checkOwnershipPath(c, h, "subjectPath", h.Ownership.SubjectPath)...)
		results = append(results, v.checkOwnershipPath(c, h, "resourcePath", h.Ownership.ResourcePath)...)
	}
	return results
}

// checkOwnershipPath validates a single ownership path expression (subjectPath or
// resourcePath). Empty expressions are reported as IssueRequired; non-empty but
// invalid DSL or unresolved path.<param>.* references are reported as IssueInvalid.
// Cognitive complexity ≤ 15 (split from validateFMT32 to stay within limit).
func (v *Validator) checkOwnershipPath(c *metadata.ContractMeta, h *metadata.HTTPTransportMeta, field, expr string) []ValidationResult {
	fullField := "endpoints.http.ownership." + field
	if expr == "" {
		return []ValidationResult{
			v.newResult(codeFMT32, SeverityError, IssueRequired, contractFile(c), fullField,
				fmt.Sprintf("contract %q has auth.serviceOwned=true but ownership.%s is empty", c.ID, field)),
		}
	}
	if !metadata.OwnershipPathValid(expr) {
		return []ValidationResult{
			v.newResult(codeFMT32, SeverityError, IssueInvalid, contractFile(c), fullField,
				fmt.Sprintf("contract %q ownership.%s %q is not a valid DSL path expression (expected ctx.<seg> or path.<seg>...)", c.ID, field, expr)),
		}
	}
	// path.<param>.* — verify <param> is declared in pathParams.
	const pathPrefix = "path."
	if strings.HasPrefix(expr, pathPrefix) {
		rest := expr[len(pathPrefix):]
		dotIdx := strings.Index(rest, ".")
		var param string
		if dotIdx >= 0 {
			param = rest[:dotIdx]
		} else {
			param = rest
		}
		if len(h.PathParams) == 0 || h.PathParams[param].Type == "" {
			return []ValidationResult{
				v.newResult(codeFMT32, SeverityError, IssueInvalid, contractFile(c), fullField,
					fmt.Sprintf(
						"contract %q ownership.%s %q references path param %q which is not declared in endpoints.http.pathParams",
						c.ID, field, expr, param,
					)),
			}
		}
	}
	return nil
}

// =============================================================================
// REF-12 — schema ref file existence (relocated from rules_ref.go in
// PR-FUNNEL-03; the check is I/O-flavored and pairs with the FMT cluster's
// disk-format rules rather than the REF cluster's metadata-graph rules).
// =============================================================================

// validateREF12 checks that contract.schemaRefs files exist on disk.
// Skipped when root is empty.
func (v *Validator) validateREF12() []ValidationResult {
	if v.root == "" {
		return nil
	}
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		results = append(results, v.checkREF12Contract(c)...)
	}
	return results
}

// checkREF12Contract validates all schema refs declared by a single contract.
func (v *Validator) checkREF12Contract(c *metadata.ContractMeta) []ValidationResult {
	var results []ValidationResult
	for _, ref := range metadata.ContractSchemaRefs(c) {
		if ref.Ref == "" {
			continue
		}
		resolved, err := metadata.ResolveContractSchemaRef(v.root, c, ref)
		if err != nil {
			results = append(results, v.newResult(
				codeREF12, SeverityError, IssueInvalid,
				contractFile(c),
				ref.Field,
				fmt.Sprintf("contract %q %s %q: %v; fix: ensure the schema ref path is valid and the file exists", c.ID, ref.Field, ref.Ref, err),
			))
			continue
		}
		if !v.fileExists(resolved.AbsPath) {
			results = append(results, v.newResult(
				codeREF12, SeverityError, IssueRefNotFound,
				contractFile(c),
				ref.Field,
				fmt.Sprintf("contract %q %s points to missing file %q;"+
					" fix: create the referenced schema file or correct the path", c.ID, ref.Field, ref.Ref),
			))
		}
	}
	return results
}
