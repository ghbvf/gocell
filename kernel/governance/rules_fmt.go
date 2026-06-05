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
		string(cellvocab.ContractLifecycleDraft):      true,
		string(cellvocab.ContractLifecycleActive):     true,
		string(cellvocab.ContractLifecycleDeprecated): true,
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
	// validRoles / validKinds derive from the cellvocab single source so the
	// accepted sets cannot drift from the typed consts, the schema enums
	// (byte-locked by TestSchemaConstantsMatchSchemaLiterals), or the parser —
	// new kinds/roles (saga, webhook, …) flow through automatically.
	validRoles = func() map[string]bool {
		m := make(map[string]bool, len(cellvocab.AllContractRoles()))
		for _, r := range cellvocab.AllContractRoles() {
			m[string(r)] = true
		}
		return m
	}()
	validKinds = func() map[string]bool {
		m := make(map[string]bool, len(cellvocab.AllContractKinds()))
		for _, k := range cellvocab.AllContractKinds() {
			m[string(k)] = true
		}
		return m
	}()
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
			results = append(results, v.newError(
				codeFMT01, IssueInvalid,
				contractFile(c),
				"lifecycle",
				fmt.Sprintf("contract %q lifecycle %q is not valid (must be draft, active, or deprecated)", c.ID, c.Lifecycle),
				"set lifecycle to draft, active, or deprecated",
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
				results = append(results, v.newError(
					codeFMT24, IssueRequired,
					file,
					"lifecycle",
					fmt.Sprintf("journey %q lifecycle is required (must be active or experimental)", j.ID),
					"add lifecycle: active or lifecycle: experimental to the journey",
				))
			} else {
				results = append(results, v.newError(
					codeFMT24, IssueInvalid,
					file,
					"lifecycle",
					fmt.Sprintf("journey %q lifecycle %q is not valid (must be active or experimental)", j.ID, j.Lifecycle),
					"set lifecycle to active or experimental",
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
		return []ValidationResult{v.newError(
			codeFMT24, IssueInvalid,
			file,
			modeField,
			fmt.Sprintf("journey %q passCriteria[%d].mode %q is not valid (must be auto or manual)", j.ID, i, pc.Mode),
			"set mode to auto or manual",
		)}
	}
	if pc.Mode == "auto" && strings.TrimSpace(pc.CheckRef) == "" {
		return []ValidationResult{v.newError(
			codeFMT24, IssueRequired,
			file,
			fmt.Sprintf(fieldCritCheckRefTmpl, i),
			fmt.Sprintf("journey %q auto passCriteria[%d] requires checkRef", j.ID, i),
			"add a checkRef pointing to a test target",
		)}
	}
	if pc.Mode == "manual" && strings.TrimSpace(pc.CheckRef) != "" {
		return []ValidationResult{v.newError(
			codeFMT24, IssueForbidden,
			file,
			fmt.Sprintf(fieldCritCheckRefTmpl, i),
			fmt.Sprintf("journey %q manual passCriteria[%d] must not declare checkRef", j.ID, i),
			"remove checkRef from this manual passCriteria entry",
		)}
	}
	return nil
}

// validateFMT02 checks that cell.type is one of {core, edge, support}.
func (v *Validator) validateFMT02() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Cells {
		if !validCellTypes[c.Type] {
			results = append(results, v.newError(
				codeFMT02, IssueInvalid,
				cellFile(c),
				"type",
				fmt.Sprintf("cell %q type %q is not valid (must be core, edge, or support)", c.ID, c.Type),
				"set type to core, edge, or support",
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
			results = append(results, v.newError(
				codeFMT03, IssueInvalid,
				cellFile(c),
				"consistencyLevel",
				fmt.Sprintf("cell %q consistencyLevel %q is not valid (must be L0-L4)", c.ID, c.ConsistencyLevel),
				"set consistencyLevel to L0, L1, L2, L3, or L4",
			))
		}
	}
	for _, c := range v.project.Contracts {
		if _, err := cellvocab.ParseLevel(c.ConsistencyLevel); err != nil {
			results = append(results, v.newError(
				codeFMT03, IssueInvalid,
				contractFile(c),
				"consistencyLevel",
				fmt.Sprintf("contract %q consistencyLevel %q is not valid (must be L0-L4)", c.ID, c.ConsistencyLevel),
				"set consistencyLevel to L0, L1, L2, L3, or L4",
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
				results = append(results, v.newError(
					codeFMT04, IssueRequired,
					contractFile(c),
					"replayable",
					fmt.Sprintf("%s contract %q must specify replayable", c.Kind, c.ID),
					"add replayable: true or replayable: false to the contract",
				))
			}
		}

		// Only event contracts require idempotencyKey and deliverySemantics.
		if kind == cellvocab.ContractEvent {
			if c.IdempotencyKey == "" {
				results = append(results, v.newError(
					codeFMT04, IssueRequired,
					contractFile(c),
					"idempotencyKey",
					fmt.Sprintf("event contract %q must specify idempotencyKey", c.ID),
					"add an idempotencyKey field to the event contract",
				))
			}
			if c.DeliverySemantics == "" {
				results = append(results, v.newError(
					codeFMT04, IssueRequired,
					contractFile(c),
					"deliverySemantics",
					fmt.Sprintf("event contract %q must specify deliverySemantics", c.ID),
					"add deliverySemantics: at-least-once or at-most-once",
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
				results = append(results, v.newError(
					codeFMT05, IssueInvalid,
					sliceFile(s),
					fmt.Sprintf("contractUsages[%d].role", i),
					fmt.Sprintf("role %q is not a valid contract role", cu.Role),
					"use one of the valid contract roles",
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
			results = append(results, v.newError(
				codeFMT06, IssueRequired,
				cellFile(c),
				"schema.primary",
				fmt.Sprintf("non-L0 cell %q must have schema.primary", c.ID),
				"add schema.primary pointing to the primary schema file",
			))
		}
	}
	return results
}

// validateFMT07 checks that the contract provider endpoint is populated based on kind.
// kind=saga shares the "endpoints.server" anchor with kind=grpc: both kinds declare
// their provider cell in endpoints.server (the saga coordinator is the server-side
// provider that orchestrates the workflow).
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
			case cellvocab.ContractWebhook:
				// Webhook provider is the explicitly-declared ownerCell (see
				// ContractMeta.ProviderEndpoint godoc). Receivers/Dispatchers are
				// derived fields populated after parse time and cannot be the
				// canonical provider field for FMT-07.
				field = "ownerCell"
			case cellvocab.ContractGRPC, cellvocab.ContractSaga:
				field = "endpoints.server"
			default:
				field = "endpoints"
			}
			results = append(results, v.newError(
				codeFMT07, IssueRequired,
				contractFile(c),
				field,
				fmt.Sprintf("contract %q (kind %q) must have a provider endpoint", c.ID, c.Kind),
				"add the required endpoint (server/publisher/handler/provider for http/event/command/projection; ownerCell for webhook)",
			))
		}
	}
	return results
}

// allKindNames returns a comma-separated list of all valid contract kinds,
// derived from cellvocab.AllContractKinds() so the message can never drift.
func allKindNames() string {
	kinds := cellvocab.AllContractKinds()
	names := make([]string, len(kinds))
	for i, k := range kinds {
		names[i] = string(k)
	}
	return strings.Join(names, ", ")
}

// validateFMT09 checks that contract.kind is one of the valid kinds defined in cellvocab.
// The accepted set is derived from cellvocab.AllContractKinds() so the message never drifts
// from the actual accepted set.
func (v *Validator) validateFMT09() []ValidationResult {
	var results []ValidationResult
	kindList := allKindNames()
	for _, c := range v.project.Contracts {
		if !validKinds[c.Kind] {
			results = append(results, v.newError(
				codeFMT09, IssueInvalid,
				contractFile(c),
				"kind",
				fmt.Sprintf("contract %q kind %q is not valid (must be one of: %s)", c.ID, c.Kind, kindList),
				fmt.Sprintf("set kind to one of: %s", kindList),
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
			results = append(results, v.newError(
				codeFMT10, IssueForbidden,
				cellFile(c),
				"id",
				fmt.Sprintf("cell ID %q is a banned legacy field name; use %q instead", c.ID, replacement),
				"rename the cell to use the replacement field name",
			))
		}
	}

	// Check contract IDs for slash-separated format (should be dot-separated).
	for _, c := range v.project.Contracts {
		if strings.Contains(c.ID, "/") {
			results = append(results, v.newError(
				codeFMT10, IssueInvalid,
				contractFile(c),
				"id",
				fmt.Sprintf("contract ID %q uses slash separator; must use dot-separated format (e.g., kind.domain.version)", c.ID),
				"rename the contract id to use dots as separators",
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
			results = append(results, v.newError(
				codeFMT08, IssueInvalid,
				contractFile(c),
				"id",
				fmt.Sprintf("contract ID %q format is invalid (missing '.' separator)", c.ID),
				"use format kind.domain.version",
			))
			continue
		}
		prefix := parts[0]
		if prefix != c.Kind {
			results = append(results, v.newError(
				codeFMT08, IssueMismatch,
				contractFile(c),
				"kind",
				fmt.Sprintf("contract %q ID prefix %q does not match kind %q", c.ID, prefix, c.Kind),
				"ensure the contract id starts with the contract kind",
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
			results = append(results, v.newError(
				codeFMT11, IssueRequired,
				cellFile(c),
				"owner.team",
				fmt.Sprintf("cell %q must have owner.team", c.ID),
				"add owner.team to the cell.yaml",
			))
		}
		if c.Owner.Role == "" {
			results = append(results, v.newError(
				codeFMT11, IssueRequired,
				cellFile(c),
				"owner.role",
				fmt.Sprintf("cell %q must have owner.role", c.ID),
				"add owner.role to the cell.yaml",
			))
		}
		if len(c.Verify.Smoke) == 0 {
			results = append(results, v.newError(
				codeFMT11, IssueRequired,
				cellFile(c),
				"verify.smoke",
				fmt.Sprintf("cell %q must have at least one verify.smoke entry", c.ID),
				"add a verify.smoke entry pointing to a smoke test",
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
			results = append(results, v.newError(
				codeFMT12, IssueRequired,
				sliceFile(s),
				"verify.unit",
				fmt.Sprintf("slice %q must have at least one verify.unit entry", s.ID),
				"add a verify.unit entry pointing to a unit test",
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
			results = append(results, v.newError(
				codeFMT13, IssueRequired,
				contractFile(c),
				"endpoints.http",
				fmt.Sprintf(advHintFMT13MissingHTTP, c.ID),
				advHintFMT13MissingHTTPFix,
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
		return []ValidationResult{v.newError(
			codeFMT13, IssueInvalid,
			file,
			"endpoints.http",
			fmt.Sprintf("contract %q can only declare endpoints.http when kind is http", c.ID),
			"remove endpoints.http or change the contract kind to http",
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
			results = append(results, v.newError(
				codeFMT13, IssueRequired,
				file,
				"endpoints.http.pathParams",
				fmt.Sprintf(advHintFMT13MissingPathParam, c.ID, name),
				advHintFMT13MissingPathParamFix,
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
				results = append(results, v.newError(
					codeFMT13, IssueInvalid,
					file,
					fmt.Sprintf("endpoints.http.pathParams.%s", name),
					fmt.Sprintf("http contract %q declares pathParams.%s but path %q has no such placeholder", c.ID, name, h.Path),
					"remove the undeclared pathParam or add the placeholder to the path",
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
		results = append(results, v.newError(
			codeFMT13, IssueRequired,
			file,
			fieldBase+".type",
			fmt.Sprintf("http contract %q %s.%s must specify type", c.ID, kind, name),
			"add a type field (string, integer, boolean, number)",
		))
	} else if !metadata.ParamTypes[p.Type] {
		results = append(results, v.newError(
			codeFMT13, IssueInvalid,
			file,
			fieldBase+".type",
			fmt.Sprintf("http contract %q %s.%s type %q is not supported", c.ID, kind, name, p.Type),
			"use one of string, integer, boolean, or number",
		))
	}

	if isPath && p.Required != nil && !*p.Required {
		results = append(results, v.newError(
			codeFMT13, IssueMismatch,
			file,
			fieldBase+".required",
			fmt.Sprintf("http contract %q pathParams.%s cannot be optional; path placeholders are required by definition", c.ID, name),
			"remove the required: false field or leave required unset",
		))
	}

	return results
}

func (v *Validator) validateFMT13Method(c *metadata.ContractMeta, h *metadata.HTTPTransportMeta, file string) []ValidationResult {
	if h.Method == "" {
		return []ValidationResult{v.newError(
			codeFMT13, IssueRequired,
			file,
			"endpoints.http.method",
			fmt.Sprintf("http contract %q must specify endpoints.http.method once endpoints.http is present", c.ID),
			"add method: GET/POST/PUT/PATCH/DELETE",
		)}
	}
	if !validHTTPMethods[strings.ToUpper(h.Method)] {
		return []ValidationResult{v.newError(
			codeFMT13, IssueInvalid,
			file,
			"endpoints.http.method",
			fmt.Sprintf("http contract %q method %q is not supported", c.ID, h.Method),
			"use one of GET, POST, PUT, PATCH, DELETE",
		)}
	}
	return nil
}

func (v *Validator) validateFMT13Path(c *metadata.ContractMeta, h *metadata.HTTPTransportMeta, file string) []ValidationResult {
	if h.Path == "" {
		return []ValidationResult{v.newError(
			codeFMT13, IssueRequired,
			file,
			"endpoints.http.path",
			fmt.Sprintf("http contract %q must specify endpoints.http.path once endpoints.http is present", c.ID),
			"add path starting with /",
		)}
	}
	if !strings.HasPrefix(h.Path, "/") {
		return []ValidationResult{v.newError(
			codeFMT13, IssueInvalid,
			file,
			"endpoints.http.path",
			fmt.Sprintf("http contract %q path %q must start with '/'", c.ID, h.Path),
			"ensure the path begins with /",
		)}
	}
	return nil
}

func (v *Validator) validateFMT13Status(c *metadata.ContractMeta, h *metadata.HTTPTransportMeta, file string) []ValidationResult {
	if h.SuccessStatus == 0 {
		return []ValidationResult{v.newError(
			codeFMT13, IssueRequired,
			file,
			"endpoints.http.successStatus",
			fmt.Sprintf("http contract %q must specify endpoints.http.successStatus once endpoints.http is present", c.ID),
			"add successStatus: 200 or another 2xx code",
		)}
	}
	if h.SuccessStatus < 200 || h.SuccessStatus > 299 {
		return []ValidationResult{v.newError(
			codeFMT13, IssueInvalid,
			file,
			"endpoints.http.successStatus",
			fmt.Sprintf("http contract %q successStatus %d must be a 2xx code", c.ID, h.SuccessStatus),
			"use a status code in the range 200-299",
		)}
	}
	return nil
}

func (v *Validator) validateFMT13NoContent(c *metadata.ContractMeta, h *metadata.HTTPTransportMeta, file string) []ValidationResult {
	var results []ValidationResult

	if h.NoContent {
		if h.SuccessStatus != 0 && h.SuccessStatus != 204 {
			results = append(results, v.newError(
				codeFMT13, IssueMismatch,
				file,
				"endpoints.http.noContent",
				fmt.Sprintf("http contract %q with noContent=true must use successStatus 204", c.ID),
				"change successStatus to 204 or remove noContent",
			))
		}
		if c.SchemaRefs.Response != "" {
			results = append(results, v.newError(
				codeFMT13, IssueForbidden,
				file,
				fieldSchemaRefsResponse,
				fmt.Sprintf("http contract %q with noContent=true must not declare schemaRefs.response", c.ID),
				"remove schemaRefs.response for no-content responses",
			))
		}
	} else if h.SuccessStatus == 204 {
		results = append(results, v.newError(
			codeFMT13, IssueMismatch,
			file,
			"endpoints.http.noContent",
			fmt.Sprintf("http contract %q with successStatus 204 must set noContent=true", c.ID),
			"add noContent: true to the endpoint",
		))
	}

	// Advisory: noContent=false without schemaRefs.response is likely incomplete.
	if !h.NoContent && c.SchemaRefs.Response == "" {
		results = append(results, v.newWarning(
			codeFMT13, IssueRequired,
			file,
			fieldSchemaRefsResponse,
			fmt.Sprintf("http contract %q with noContent=false declares no schemaRefs.response", c.ID),
			"declare endpoints.http.schemaRefs.response, or set noContent: true if the endpoint returns no body",
		))
	}

	return results
}

// validateFMT37 checks gRPC transport metadata (endpoints.grpc) — the grpc-kind
// sibling of FMT-13 (HTTP transport). Two cases are checked, mirroring FMT-13:
//   - kind=grpc with nil endpoints.grpc → Error: required block missing.
//   - any kind with non-nil endpoints.grpc → delegate to validateFMT37ForContract,
//     which rejects non-grpc contracts declaring endpoints.grpc and validates the
//     block's internal consistency for grpc contracts.
//
// Internal consistency mirrors the contract.schema.json grpc if/then block and
// kernel/contractspec.validateGRPC; the streamingType enum and proto prefix are
// single-sourced from metadata.GRPCStreamingTypeEnum / metadata.GRPCProtoPathPrefix
// so schema, governance, and runtime never drift on the accepted value sets.
//
// Without this rule, schema-aware tooling validates endpoints.grpc but
// `gocell validate` accepts any grpc block (service/method/proto missing,
// out-of-enum streamingType, proto outside contracts/grpc/), and a non-grpc
// contract could silently carry endpoints.grpc — leaving CLI users a different
// contract than the schema declares (review C1).
//
// AI-robust: Medium (governance YAML-metadata validate layer, same tier as
// FMT-13). Value-presence ceiling; the streamingType/proto value sets are
// Hard-locked to the schema literal via TestSchemaConstantsMatchSchemaLiterals.
func (v *Validator) validateFMT37() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		isGRPC := cellvocab.ContractKind(c.Kind) == cellvocab.ContractGRPC
		if isGRPC && c.Endpoints.GRPC == nil {
			results = append(results, v.newError(
				codeFMT37, IssueRequired,
				contractFile(c),
				"endpoints.grpc",
				fmt.Sprintf("grpc contract %q must declare endpoints.grpc", c.ID),
				"add endpoints.grpc with service, method, and proto",
			))
			continue
		}
		if c.Endpoints.GRPC == nil {
			// Non-grpc contract without endpoints.grpc — nothing to validate.
			continue
		}
		// endpoints.grpc is non-nil: validate it (validateFMT37ForContract also
		// rejects non-grpc contracts that erroneously declare endpoints.grpc).
		results = append(results, v.validateFMT37ForContract(c)...)
	}
	return results
}

// validateFMT37ForContract validates a single contract's gRPC transport metadata.
func (v *Validator) validateFMT37ForContract(c *metadata.ContractMeta) []ValidationResult {
	g := c.Endpoints.GRPC
	file := contractFile(c)

	if cellvocab.ContractKind(c.Kind) != cellvocab.ContractGRPC {
		return []ValidationResult{v.newError(
			codeFMT37, IssueInvalid,
			file,
			"endpoints.grpc",
			fmt.Sprintf("contract %q can only declare endpoints.grpc when kind is grpc", c.ID),
			"remove endpoints.grpc or change the contract kind to grpc",
		)}
	}

	var results []ValidationResult
	if g.Service == "" {
		results = append(results, v.newError(
			codeFMT37, IssueRequired, file, "endpoints.grpc.service",
			fmt.Sprintf("grpc contract %q must specify endpoints.grpc.service", c.ID),
			"add service: the proto fully-qualified service name (e.g. device.command.v1.DeviceCommandService)",
		))
	}
	if g.Method == "" {
		results = append(results, v.newError(
			codeFMT37, IssueRequired, file, "endpoints.grpc.method",
			fmt.Sprintf("grpc contract %q must specify endpoints.grpc.method", c.ID),
			"add method: the proto method name (e.g. IssueCommand)",
		))
	}
	results = append(results, v.validateFMT37Proto(c, g, file)...)
	results = append(results, v.validateFMT37Streaming(c, g, file)...)
	return results
}

// validateFMT37Proto enforces that endpoints.grpc.proto is present, rooted
// under metadata.GRPCProtoPathPrefix (contracts/grpc/), free of control runes,
// and a local path (no traversal). Delegates to metadata.ValidateGRPCProtoPath
// — the single-source 4-guard validator shared with contractgen — so governance
// and codegen cannot diverge on which checks are applied.
func (v *Validator) validateFMT37Proto(c *metadata.ContractMeta, g *metadata.GRPCTransportMeta, file string) []ValidationResult {
	if err := metadata.ValidateGRPCProtoPath(g.Proto); err != nil {
		issue := IssueInvalid
		if g.Proto == "" {
			issue = IssueRequired
		}
		return []ValidationResult{v.newError(
			codeFMT37, issue, file, "endpoints.grpc.proto",
			fmt.Sprintf("grpc contract %q endpoints.grpc.proto: %s", c.ID, err.Error()),
			"set proto to a local, control-rune-free path under contracts/grpc/ (no .. traversal)",
		)}
	}
	return nil
}

// validateFMT37Streaming enforces that endpoints.grpc.streamingType, when
// present, is one of metadata.GRPCStreamingTypeEnum. An omitted/empty value is
// the unary default and is accepted.
func (v *Validator) validateFMT37Streaming(c *metadata.ContractMeta, g *metadata.GRPCTransportMeta, file string) []ValidationResult {
	if g.StreamingType == "" || metadata.IsKnownGRPCStreamingType(g.StreamingType) {
		return nil
	}
	return []ValidationResult{v.newError(
		codeFMT37, IssueInvalid, file, "endpoints.grpc.streamingType",
		fmt.Sprintf("grpc contract %q streamingType %q is not one of %v", c.ID, g.StreamingType, metadata.GRPCStreamingTypeEnum),
		"use one of unary, server-stream, client-stream, bidi (or omit for unary)",
	)}
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
			results = append(results, v.newError(
				codeFMT26, IssueForbidden,
				contractFile(c),
				"endpoints.http.auth",
				fmt.Sprintf(
					"contract %q declares both auth.public and auth.passwordResetExempt; "+
						"they are mutually exclusive: public skips JWT entirely while "+
						"passwordResetExempt requires a valid JWT",
					c.ID,
				),
				"remove one of the conflicting auth flags",
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
			results = append(results, v.newError(
				codeFMT14, IssueRequired,
				sliceFile(s),
				"allowedFiles",
				fmt.Sprintf(
					"slice %q must declare explicit allowedFiles (e.g., [%q])",
					s.ID, allowedFilesExample(s),
				),
				"add allowedFiles listing the files owned by this slice",
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
		return []ValidationResult{v.newError(
			codeFMT15, IssueInvalid,
			contractFile(c), fieldSchemaRefsResponse,
			fmt.Sprintf("cannot resolve response schema for contract %q: %v", c.ID, resolveErr),
			"ensure schemaRefs.response points to a valid schema file",
		)}
	}
	data, err := v.readFile(resolved.AbsPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil // REF-12 handles missing files
		}
		return []ValidationResult{v.newError(
			codeFMT15, IssueInvalid,
			contractFile(c), fieldSchemaRefsResponse,
			fmt.Sprintf("cannot read response schema for contract %q: %v", c.ID, err),
			"ensure the response schema file exists and is readable",
		)}
	}
	info, err := parseListSchemaInfo(data)
	if err != nil {
		return []ValidationResult{v.newError(
			codeFMT15, IssueInvalid,
			contractFile(c), fieldSchemaRefsResponse,
			fmt.Sprintf("response schema for contract %q is not valid JSON: %v", c.ID, err),
			"fix the JSON syntax in the response schema file",
		)}
	}
	if hasCombinator(info) && looksLikeListSchema(info) {
		return []ValidationResult{v.newWarning(
			codeFMT15, IssueInvalid,
			contractFile(c), fieldSchemaRefsResponse,
			fmt.Sprintf("response schema for contract %q uses oneOf/anyOf/allOf:"+
				" FMT-15 cannot verify list constraints", c.ID),
			"split the response into single-shape contracts so FMT-15 can verify the list constraints",
		)}
	}
	if !isListSchema(info) {
		return nil
	}
	var results []ValidationResult
	if !hasMoreInRequired(info) {
		results = append(results, v.newError(
			codeFMT15, IssueRequired,
			contractFile(c),
			fieldSchemaRefsResponse,
			fmt.Sprintf("list response schema for contract %q must include \"hasMore\" in required fields", c.ID),
			"add \"hasMore\" to the required array in the response schema",
		))
	}
	if !hasNextCursorProperty(info) {
		results = append(results, v.newError(
			codeFMT15, IssueRequired,
			contractFile(c),
			fieldSchemaRefsResponse,
			fmt.Sprintf("list response schema for contract %q must declare \"nextCursor\" property", c.ID),
			"add a \"nextCursor\" property to the response schema",
		))
	}
	if !hasNextCursorInRequired(info) {
		results = append(results, v.newError(
			codeFMT15, IssueRequired,
			contractFile(c),
			fieldSchemaRefsResponse,
			fmt.Sprintf("list response schema for contract %q must include \"nextCursor\" in required fields", c.ID),
			"add \"nextCursor\" to the required array in the response schema",
		))
	}
	return results
}

// responseSchemaDataInfo holds the JSON Schema "data" property subset.
type responseSchemaDataInfo struct {
	Type string `json:"type"`
}

// responseSchemaPropertiesInfo holds the JSON Schema "properties" subset needed
// for list-lint checks.
type responseSchemaPropertiesInfo struct {
	Data responseSchemaDataInfo `json:"data"`
	// NextCursor is non-nil when the "nextCursor" property is declared in the schema.
	NextCursor *json.RawMessage `json:"nextCursor"`
	// HasMore is non-nil when the "hasMore" property is declared in the schema.
	HasMore *json.RawMessage `json:"hasMore"`
}

// responseSchemaInfo holds the subset of JSON Schema fields needed for list-lint checks.
type responseSchemaInfo struct {
	Properties responseSchemaPropertiesInfo `json:"properties"`
	Required   []string                     `json:"required"`
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
		results = append(results, v.newError(
			codeFMT27, IssueForbidden,
			contractFile(c),
			"endpoints.http.auth",
			fmt.Sprintf(
				"contract %q has incompatible auth mode combination: %s set to true. "+
					"Set at most one of {auth.public, auth.bootstrap, "+
					"auth.passwordResetExempt, auth.clientsOnly}; "+
					"only auth.serviceOwned may pair with auth.passwordResetExempt",
				c.ID, formatTrueAuthFields(auth),
			),
			"remove the conflicting auth mode flags",
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
		results = append(results, v.newError(
			codeFMT30, IssueInvalid,
			assemblyFile(asm),
			"build.deployTemplate",
			fmt.Sprintf(
				"assembly %q build.deployTemplate=%q is not one of %v",
				asm.ID, dt, metadata.DeployTemplateEnum,
			),
			"set build.deployTemplate to one of the allowed values",
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
			results = append(results, v.newError(
				codeFMT29, IssueRequired,
				assemblyFile(asm),
				"owner.team",
				fmt.Sprintf("assembly %q must have owner.team", asm.ID),
				"add owner.team to the assembly.yaml",
			))
		}
		if asm.Owner.Role == "" {
			results = append(results, v.newError(
				codeFMT29, IssueRequired,
				assemblyFile(asm),
				"owner.role",
				fmt.Sprintf("assembly %q must have owner.role", asm.ID),
				"add owner.role to the assembly.yaml",
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
			results = append(results, v.newError(
				codeFMT28, IssueForbidden,
				contractFile(c),
				"endpoints.http.auth.bootstrap",
				fmt.Sprintf(
					"contract %q has auth.bootstrap:true on path %q; "+
						"bootstrap auth is only permitted on setup/admin contracts "+
						"(path must match IsBootstrapPath: /api/v{N}/{cell}/setup/admin)",
					c.ID, path,
				),
				"use auth.bootstrap only on setup/admin paths",
			))
		}
		if !auth.ClientsOnly {
			continue
		}
		if !metadata.IsInternalHTTPPath(path) {
			results = append(results, v.newError(
				codeFMT28, IssueForbidden,
				contractFile(c),
				"endpoints.http.auth.clientsOnly",
				fmt.Sprintf(
					"contract %q has auth.clientsOnly:true on path %q; "+
						"clientsOnly auth is only permitted on internal HTTP paths "+
						"(path must match IsInternalHTTPPath: /internal/v1 or /internal/v1/...)",
					c.ID, path,
				),
				"move the endpoint to an /internal/v1 path or remove auth.clientsOnly",
			))
		}
		if len(c.Endpoints.Clients) == 0 {
			results = append(results, v.newError(
				codeFMT28, IssueRequired,
				contractFile(c),
				"endpoints.clients",
				fmt.Sprintf(
					"contract %q has auth.clientsOnly:true but endpoints.clients is empty; "+
						"clientsOnly auth requires at least one declared client cell",
					c.ID,
				),
				"add at least one cell id to endpoints.clients",
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
		results = append(results, v.newError(
			codeFMT31, IssueRequired,
			contractFile(c),
			"endpoints.clients",
			fmt.Sprintf(
				"internal HTTP contract %q (path %q) has empty endpoints.clients; "+
					"/internal/v1/ contracts must declare at least one caller cell "+
					"so RequireCallerCell has an allowlist",
				c.ID, c.Endpoints.HTTP.Path,
			),
			"add caller cell ids to endpoints.clients",
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
			results = append(results, v.newError(
				codeFMT32, IssueRequired,
				contractFile(c),
				"endpoints.http.ownership",
				fmt.Sprintf(
					"contract %q has auth.serviceOwned=true but no ownership block",
					c.ID,
				),
				"declare endpoints.http.ownership with subjectPath and resourcePath",
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
			v.newError(codeFMT32, IssueRequired, contractFile(c), fullField,
				fmt.Sprintf("contract %q has auth.serviceOwned=true but ownership.%s is empty", c.ID, field),
				fmt.Sprintf("set endpoints.http.ownership.%s to a ctx.* or path.* expression", field)),
		}
	}
	if !metadata.OwnershipPathValid(expr) {
		return []ValidationResult{
			v.newError(codeFMT32, IssueInvalid, contractFile(c), fullField,
				fmt.Sprintf("contract %q ownership.%s %q is not a valid DSL path expression", c.ID, field, expr),
				"use a valid path expression (ctx.<seg> or path.<param>.<seg>, camelCase segments)"),
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
		_, paramExists := h.PathParams[param]
		if len(h.PathParams) == 0 || !paramExists {
			return []ValidationResult{
				v.newError(codeFMT32, IssueInvalid, contractFile(c), fullField,
					fmt.Sprintf(
						"contract %q ownership.%s %q references path param %q not declared in endpoints.http.pathParams",
						c.ID, field, expr, param,
					),
					fmt.Sprintf("declare %q under endpoints.http.pathParams or correct the path expression", param)),
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
			results = append(results, v.newError(
				codeREF12, IssueInvalid,
				contractFile(c),
				ref.Field,
				fmt.Sprintf("contract %q %s %q: %v", c.ID, ref.Field, ref.Ref, err),
				"ensure the schema ref path is valid and the file exists",
			))
			continue
		}
		if !v.fileExists(resolved.AbsPath) {
			results = append(results, v.newError(
				codeREF12, IssueRefNotFound,
				contractFile(c),
				ref.Field,
				fmt.Sprintf("contract %q %s points to missing file %q", c.ID, ref.Field, ref.Ref),
				"create the referenced schema file or correct the path",
			))
		}
	}
	return results
}

// validateFMT33 enforces SLICE-HTTP-VISIBILITY-SEGREGATION-01: a single slice
// must not serve both a public HTTP contract (path under /api/*) and an
// internal HTTP contract (path matching metadata.IsInternalHTTPPath, i.e.
// /internal/v1). Public-facing and internal control-plane endpoints sit on
// different trust boundaries (distinct listeners, auth chains, and failure
// domains; the public surface authenticates external principals while the
// internal surface enforces caller-cell allowlists). Co-locating both surfaces
// in one slice couples two trust boundaries into one deployable/ownership unit
// and makes the "same struct, two register paths" mistake expressible. Both
// visibility classes route through metadata oracles (IsPublicHTTPPath /
// IsInternalHTTPPath) — no inline string anchor. IsPublicHTTPPath covers all
// /api/vN versions (not locked to v1), ensuring future /api/v2 endpoints on
// the public surface are caught by the same rule.
//
// Scope: only role=serve HTTP usages participate (a slice calling an internal
// contract as a client is unrelated). Paths that match neither oracle (e.g.
// bootstrap /healthz) set no flag and never trigger; the rule fires only when
// one slice holds at least one of each.
//
// Membership guard: TestAllRulesMatchGolden locks FMT-33 in
// goldenRuleIDs — deleting the allRules registration turns CI red.
//
// AI-robust: Medium (governance YAML-metadata validate layer, same tier as
// FMT-31/ADV-06). Both visibility classes route through metadata oracles
// (IsPublicHTTPPath / IsInternalHTTPPath) — no inline string anchor.
//
// Fix pattern: move shared logic to cells/{cell}/internal/{domain}/, create
// one public slice (/api) and one internal slice (/internal/v1), each
// type-aliasing the shared service. Canonical reference: configread +
// configreadinternal sharing cells/configcore/internal/configreader.
func (v *Validator) validateFMT33() []ValidationResult {
	var results []ValidationResult
	for _, s := range v.project.Slices {
		if v.sliceMixesHTTPVisibility(s) {
			results = append(results, v.newError(
				codeFMT33, IssueForbidden,
				sliceFile(s),
				"contractUsages",
				fmt.Sprintf(
					"slice %q serves both public (/api/v1) and internal (/internal/v1) HTTP "+
						"contracts; public and internal API surfaces are distinct trust "+
						"boundaries and must be segregated into separate slices",
					s.ID,
				),
				"split into a public slice and an internal slice",
			))
		}
	}
	return results
}

// validateFMT34 forbids auth.public / auth.passwordResetExempt on
// /internal/v1/* paths. Internal endpoints must not bypass JWT entirely
// (auth.public) or accept JWTs carrying password_reset_required=true
// (auth.passwordResetExempt); use auth.serviceOwned (service delegates
// ownership) or auth.clientsOnly (caller-cell allowlist) instead.
// auth.bootstrap is intentionally NOT suggested — FMT-28 narrows
// bootstrap to /api/v{N}/{cell}/setup/admin, so it is not a valid
// alternative on /internal/v1/* paths.
//
// Funnel pair (ai-robust.md §Funnel 双向锁评级):
//
//	upstream Hard: tools/codegen/contractgen/builder.go::validateAuthOnInternalPath,
//	               called unconditionally inside buildHTTPEndpointSpec. The "sole
//	               HTTP codegen entry" premise is enforced — not grep-asserted — by
//	               the sealed (unexported) httpEndpointSpec type AND the
//	               package-private build/render surface: no out-of-package code
//	               can construct a non-nil Endpoint (A1a) nor obtain a mutable
//	               spec to flip Path/AuthPublic/Clients after FMT-34 ran (A4) —
//	               cross-package bypass not expressible. Locked by archtest
//	               CODEGEN-BUILDHTTPENDPOINTSPEC-SOLE-CALLER-01 (intra-package sole
//	               constructor/caller + http.Handler emit-uniqueness across
//	               tools/codegen/** + no exported spec leak).
//	downstream:    this rule (Medium — receiver-type + RuleCode const + fix suffix archtest)
//
// Orthogonal to FMT-26 (two-bypass mutex, path-agnostic) and
// runtime/auth/route.go validateBypassCompatibility (Route struct field
// mutex, path-agnostic) — three-layer defense, metadata-first.
//
// FMT-34 emits one finding per violating flag (parallel to FMT-28's
// multi-finding shape) so the report points at each offending field.
func (v *Validator) validateFMT34() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if c.Endpoints.HTTP == nil {
			continue
		}
		path := c.Endpoints.HTTP.Path
		if !metadata.IsInternalHTTPPath(path) {
			continue
		}
		auth := c.Endpoints.HTTP.Auth
		if auth.Public {
			results = append(results, v.newError(
				codeFMT34, IssueForbidden,
				contractFile(c),
				"endpoints.http.auth.public",
				fmt.Sprintf(
					"contract %q declares auth.public:true on internal path %q; "+
						"internal endpoints must not bypass JWT (use auth.serviceOwned "+
						"or auth.clientsOnly instead)",
					c.ID, path,
				),
				"remove auth.public or move the endpoint off /internal/v1/",
			))
		}
		if auth.PasswordResetExempt {
			results = append(results, v.newError(
				codeFMT34, IssueForbidden,
				contractFile(c),
				"endpoints.http.auth.passwordResetExempt",
				fmt.Sprintf(
					"contract %q declares auth.passwordResetExempt:true on internal "+
						"path %q; internal endpoints are cell-to-cell only and must "+
						"not accept the password-reset bypass token",
					c.ID, path,
				),
				"remove auth.passwordResetExempt or move the endpoint off /internal/v1/",
			))
		}
	}
	return results
}

// validateFMT36 enforces that every cell's `requires` list contains only values
// from metadata.CapabilityEnum (closed enum: postgres, redis, rabbitmq) and that
// no value appears more than once (uniqueItems). cell.requires is the
// authoritative single source from which an assembly's provisioned capability
// set is derived (Design Y, #855); the schema literal at
// schemas/cell.schema.json properties.requires.items.enum is kept byte-equal to
// metadata.CapabilityEnum by TestSchemaConstantsMatchSchemaLiterals; governance
// is the sole runtime gatekeeper that rejects out-of-enum and duplicate values.
//
// Without this rule, schema-aware tooling rejects unknown/duplicate values but
// `gocell validate` accepts them, leaving CLI users with a different contract
// than the schema declares. Mirrors the pattern established by validateFMT30
// for build.deployTemplate.
//
// A subset check (requires ⊆ assembly.capabilities) is intentionally absent:
// the assembly set is the union of cell requires, so subset holds structurally.
func (v *Validator) validateFMT36() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Cells {
		if c == nil {
			continue
		}
		seen := make(map[string]bool, len(c.Requires))
		for i, cap := range c.Requires {
			field := fmt.Sprintf("requires[%d]", i)
			if !metadata.IsKnownCapability(cap) {
				results = append(results, v.newError(
					codeFMT36, IssueInvalid,
					cellFile(c),
					field,
					fmt.Sprintf(
						"cell %q requires[%d]=%q is not one of %v",
						c.ID, i, cap, metadata.CapabilityEnum,
					),
					"set requires items to one of the allowed values: postgres, redis, rabbitmq",
				))
				continue
			}
			if seen[cap] {
				results = append(results, v.newError(
					codeFMT36, IssueDuplicate,
					cellFile(c),
					field,
					fmt.Sprintf(
						"cell %q requires[%d]=%q is a duplicate; each capability may appear at most once",
						c.ID, i, cap,
					),
					"remove the duplicate requires entry",
				))
				continue
			}
			seen[cap] = true
		}
	}
	return results
}

// validateFMT38 enforces webhook contract-side required fields at
// `gocell validate` time — the live parity counterpart to FMT-04 (event/projection
// required fields). The same constraints live in contract.schema.json's
// kind==webhook if/then block, but that schema is not run by `gocell validate`
// (see kernel/metadata/schemas/embed.go — "planned Phase 2"). Without this rule
// an inbound webhook contract that omits its signature/payload block, or
// declares an unsupported signature algorithm, passes `gocell validate`
// silently (fail-open), unlike its event counterpart.
//
// (FMT-37 is the sibling grpc-transport rule; webhook landed second and took the
// next free code FMT-38.)
//
//   - direction==inbound → signature block AND payload block required (the
//     receiver landing in PR-3 cannot verify without them).
//   - direction==inbound → signature.toleranceSeconds MUST be >= 1 (0 disables
//     the replay-attack window — fail-open) AND payload.maxBodyBytes MUST be
//     >= 1 (0 is an unbounded-body DoS surface). The runtime HMAC verifier
//     likewise rejects a non-positive tolerance.
//   - direction==inbound → every signed-string ingredient on the (now-required)
//     signature block MUST be non-empty: signature.deliveryIDHeader,
//     signature.timestampHeader, signature.signatureHeader, and
//     signature.signedStringForm. A partially-hollow shell (block present but
//     headers/template empty) leaves the PR-3 runtime HMAC verifier unable to
//     build the signed string → fail-open; reject it at declaration.
//   - direction==inbound → payload.contentType on the (now-required) payload
//     block MUST be non-empty so the receiver can enforce a Content-Type on
//     incoming deliveries. payload.schemaRef stays optional.
//   - whenever a signature block is present (inbound: required; outbound:
//     optional), signature.algorithm MUST equal the sole supported value
//     hmac-sha256 — no downgrade path.
//
// direction itself is validated fail-closed at parse time
// (kernel/metadata.finalizeWebhookContract), so a bad/empty direction never
// reaches this rule.
//
// fmt38WebhookAlgorithm is the sole supported signature algorithm. It is a
// local literal rather than an import of kernel/webhook.AlgorithmHMACSHA256:
// kernel/governance must not depend on kernel/webhook (KERNEL-INTERNAL-DAG-01
// forbids the governance→webhook edge). The value is kept in lock-step with the
// runtime const by the test-only cross-check TestFMT38AlgorithmMatchesKernel
// (a _test.go import of kernel/webhook is not a production DAG edge).
const fmt38WebhookAlgorithm = "hmac-sha256"

func (v *Validator) validateFMT38() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if c == nil || c.Kind != string(cellvocab.ContractWebhook) {
			continue
		}
		if c.Direction == string(cellvocab.DirectionInbound) {
			results = append(results, v.fmt38InboundChecks(c)...)
		}
		if c.Signature != nil && c.Signature.Algorithm != fmt38WebhookAlgorithm {
			results = append(results, v.newError(
				codeFMT38, IssueInvalid,
				contractFile(c), "signature.algorithm",
				fmt.Sprintf("webhook contract %q signature.algorithm=%q is not a supported value", c.ID, c.Signature.Algorithm),
				fmt.Sprintf("set signature.algorithm to %q (the sole supported HMAC algorithm)", fmt38WebhookAlgorithm),
			))
		}
	}
	return results
}

// fmt38InboundChecks returns the FMT-38 findings specific to an inbound webhook
// contract: signature + payload blocks are required, their fail-open knobs
// (toleranceSeconds, maxBodyBytes) must carry a positive value, and — when the
// block is present — every field the PR-3 runtime HMAC verifier needs to build
// the signed string (delivery/timestamp/signature headers, signedStringForm,
// contentType) must be non-empty. The per-field checks are split into
// fmt38SignatureFieldChecks / fmt38PayloadFieldChecks to keep each function
// under the cognitive-complexity ceiling.
func (v *Validator) fmt38InboundChecks(c *metadata.ContractMeta) []ValidationResult {
	var results []ValidationResult
	switch {
	case c.Signature == nil:
		results = append(results, v.newError(
			codeFMT38, IssueRequired,
			contractFile(c), "signature",
			fmt.Sprintf("inbound webhook contract %q must declare a signature block", c.ID),
			"add a signature block (algorithm, headers, signedStringForm) to the inbound webhook contract",
		))
	case c.Signature.ToleranceSeconds < 1:
		results = append(results, v.newError(
			codeFMT38, IssueInvalid,
			contractFile(c), "signature.toleranceSeconds",
			fmt.Sprintf("inbound webhook contract %q signature.toleranceSeconds=%d must be >= 1; "+
				"0 disables the replay-window check (fail-open)", c.ID, c.Signature.ToleranceSeconds),
			"set signature.toleranceSeconds to a positive number of seconds (e.g. 300); "+
				"the runtime HMAC verifier also requires a positive tolerance",
		))
	}
	results = append(results, v.fmt38SignatureFieldChecks(c)...)
	switch {
	case c.Payload == nil:
		results = append(results, v.newError(
			codeFMT38, IssueRequired,
			contractFile(c), "payload",
			fmt.Sprintf("inbound webhook contract %q must declare a payload block", c.ID),
			"add a payload block (contentType, maxBodyBytes) to the inbound webhook contract",
		))
	case c.Payload.MaxBodyBytes < 1:
		results = append(results, v.newError(
			codeFMT38, IssueInvalid,
			contractFile(c), "payload.maxBodyBytes",
			fmt.Sprintf("inbound webhook contract %q payload.maxBodyBytes=%d must be >= 1; "+
				"0 means an unbounded request body (DoS surface)", c.ID, c.Payload.MaxBodyBytes),
			"set payload.maxBodyBytes to a positive byte limit (e.g. 1048576 for 1 MB)",
		))
	}
	results = append(results, v.fmt38PayloadFieldChecks(c)...)
	return results
}

// fmt38SignatureFieldChecks flags each empty signed-string ingredient on an
// inbound webhook signature block (delivery/timestamp/signature headers,
// signedStringForm). Without all four the PR-3 runtime HMAC verifier cannot
// build the signed string and the route fails open. Only runs when a signature
// block is present (the missing-block case is handled by fmt38InboundChecks).
func (v *Validator) fmt38SignatureFieldChecks(c *metadata.ContractMeta) []ValidationResult {
	if c.Signature == nil {
		return nil
	}
	type fieldCheck struct {
		value string
		field string
		human string
	}
	checks := []fieldCheck{
		{c.Signature.DeliveryIDHeader, "signature.deliveryIDHeader", "deliveryIDHeader"},
		{c.Signature.TimestampHeader, "signature.timestampHeader", "timestampHeader"},
		{c.Signature.SignatureHeader, "signature.signatureHeader", "signatureHeader"},
		{c.Signature.SignedStringForm, "signature.signedStringForm", "signedStringForm"},
	}
	var results []ValidationResult
	for _, ch := range checks {
		if ch.value != "" {
			continue
		}
		results = append(results, v.newError(
			codeFMT38, IssueRequired,
			contractFile(c), ch.field,
			fmt.Sprintf("inbound webhook contract %q must declare a non-empty signature.%s; "+
				"the runtime HMAC verifier cannot build the signed string without it (fail-open)", c.ID, ch.human),
			fmt.Sprintf("set signature.%s to the header/template value the webhook source uses", ch.human),
		))
	}
	return results
}

// fmt38PayloadFieldChecks flags an empty payload.contentType on an inbound
// webhook payload block. Without it the receiver cannot enforce a Content-Type
// on incoming deliveries. Only runs when a payload block is present (the
// missing-block case is handled by fmt38InboundChecks).
func (v *Validator) fmt38PayloadFieldChecks(c *metadata.ContractMeta) []ValidationResult {
	if c.Payload == nil || c.Payload.ContentType != "" {
		return nil
	}
	return []ValidationResult{v.newError(
		codeFMT38, IssueRequired,
		contractFile(c), "payload.contentType",
		fmt.Sprintf("inbound webhook contract %q must declare a non-empty payload.contentType; "+
			"the receiver cannot enforce a Content-Type on incoming deliveries without it", c.ID),
		"set payload.contentType to the expected MIME type (e.g. application/json)",
	)}
}

// validateFMT39 validates contract.yaml `transports` for every contract in the
// project. Three orthogonal sub-checks run in declaration order:
//
//  1. Non-empty: nil or empty Transports slice → error (parser only defaults
//     known kinds; unknown kind defaults to nil, indicating a configuration
//     error caught by FMT-09 before FMT-39, but we guard here too).
//  2. Known values: every element ∈ metadata.TransportEnum (single source shared
//     with schema and runtime); unknown value → error.
//  3. No duplicates: the same transport value must not appear more than once.
//  4. Kind-compat matrix: each contract kind restricts the accepted transport
//     set. The matrix uses cellvocab.Transport* consts to avoid bare literals:
//     - event      ⊆ {amqp, mqtt, internal}
//     - command    ⊆ {amqp, internal}
//     - projection ⊆ {internal}
//     - http       == {http}
//     - grpc       == {grpc}
//     - webhook    == {http}
//     - saga       == {internal}
//
// "⊆" means every declared element must be in the allowed set (subset).
// "==" means the set must equal exactly the singleton (no more, no less).
//
// AI-robust: Medium (governance YAML-metadata validate layer; same tier as
// FMT-36/FMT-37/FMT-38). The transport set is Hard-locked to the schema enum
// via TestSchemaConstantsMatchSchemaLiterals#transportEnum.
//
// No dedicated TRANSPORT-SEALED-FUNNEL-01 archtest — the transport→ContractSpec
// surface is already sealed by NO-MANUAL-CONTRACTSPEC-LITERAL-01 + codegen
// golden; see ADR
// docs/architecture/202606040210-1389-adr-mqtt-transports-multi-value-truth-source.md
// §P2.5.
func (v *Validator) validateFMT39() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		results = append(results, v.validateFMT39ForContract(c)...)
	}
	return results
}

// validateFMT39ForContract validates a single contract's transports field.
func (v *Validator) validateFMT39ForContract(c *metadata.ContractMeta) []ValidationResult {
	file := contractFile(c)
	var results []ValidationResult

	// 1. Non-empty guard.
	if len(c.Transports) == 0 {
		results = append(results, v.newError(
			codeFMT39, IssueRequired,
			file, "transports",
			fmt.Sprintf("contract %q (kind %q) has no transports declared; check that kind is a known value", c.ID, c.Kind),
			"add transports: [amqp] to contract.yaml (event/command→amqp, http/webhook→http, grpc→grpc, projection/saga→internal)",
		))
		return results
	}

	// 2. Unknown-value + 3. Duplicate checks.
	seen := make(map[string]bool, len(c.Transports))
	for i, t := range c.Transports {
		field := fmt.Sprintf("transports[%d]", i)
		if !metadata.IsKnownTransport(t) {
			results = append(results, v.newError(
				codeFMT39, IssueInvalid,
				file, field,
				fmt.Sprintf("contract %q transports[%d]=%q is not one of %v", c.ID, i, t, metadata.TransportEnum),
				fmt.Sprintf("use one of the allowed transport values: %s", strings.Join(metadata.TransportEnum, ", ")),
			))
			continue
		}
		if seen[t] {
			results = append(results, v.newError(
				codeFMT39, IssueDuplicate,
				file, field,
				fmt.Sprintf("contract %q transports[%d]=%q is a duplicate; each transport may appear at most once", c.ID, i, t),
				"remove the duplicate transport entry",
			))
			continue
		}
		seen[t] = true
	}
	if len(results) > 0 {
		// Skip compat matrix when basic validation failed to avoid cascading noise.
		return results
	}

	// 4. Kind↔transport compatibility matrix.
	return v.checkFMT39KindCompat(c, file)
}

// checkFMT39KindCompat enforces the kind↔transport compatibility matrix.
//
// "exact" kinds (http/webhook→http, grpc→grpc, projection/saga→internal) must
// equal exactly the named singleton because their transport is physically
// determined by the kind's protocol — there is no wire transport of their own
// that could differ.
//
// "subset" kinds (event, command) may use any sub-set of the allowed set
// because they can run over multiple brokers simultaneously.
//
// Note: command excludes mqtt deliberately — commands are request-response;
// mqtt is fire-and-forget/QoS-pub-sub, not suited for request-response.
func (v *Validator) checkFMT39KindCompat(c *metadata.ContractMeta, file string) []ValidationResult {
	kind := cellvocab.ContractKind(c.Kind)

	// Exact-match kinds: transports must equal exactly one singleton.
	exactSingleton := map[cellvocab.ContractKind]cellvocab.Transport{
		cellvocab.ContractHTTP:       cellvocab.TransportHTTP,
		cellvocab.ContractGRPC:       cellvocab.TransportGRPC,
		cellvocab.ContractWebhook:    cellvocab.TransportHTTP,
		cellvocab.ContractProjection: cellvocab.TransportInternal,
		cellvocab.ContractSaga:       cellvocab.TransportInternal,
	}
	if want, ok := exactSingleton[kind]; ok {
		if len(c.Transports) != 1 || c.Transports[0] != string(want) {
			return []ValidationResult{v.newError(
				codeFMT39, IssueMismatch,
				file, "transports",
				fmt.Sprintf(
					"contract %q (kind %q) must have transports=[%q] exactly; got %v",
					c.ID, c.Kind, string(want), c.Transports,
				),
				fmt.Sprintf("set transports: [%s] for kind=%s contracts", string(want), c.Kind),
			)}
		}
		return nil
	}

	// Subset kinds: each element must be in the allowed set.
	allowedSubsets := map[cellvocab.ContractKind][]cellvocab.Transport{
		cellvocab.ContractEvent:   {cellvocab.TransportAMQP, cellvocab.TransportMQTT, cellvocab.TransportInternal},
		cellvocab.ContractCommand: {cellvocab.TransportAMQP, cellvocab.TransportInternal},
	}
	allowed, isSubset := allowedSubsets[kind]
	if !isSubset {
		// Unknown kind — FMT-09 handles it; skip compat.
		return nil
	}
	allowedSet := make(map[string]bool, len(allowed))
	for _, t := range allowed {
		allowedSet[string(t)] = true
	}
	allowedNames := make([]string, len(allowed))
	for i, t := range allowed {
		allowedNames[i] = string(t)
	}
	var results []ValidationResult
	for i, t := range c.Transports {
		if !allowedSet[t] {
			results = append(results, v.newError(
				codeFMT39, IssueMismatch,
				file, fmt.Sprintf("transports[%d]", i),
				fmt.Sprintf(
					"contract %q (kind %q) transport %q is not compatible; allowed: %v",
					c.ID, c.Kind, t, allowedNames,
				),
				fmt.Sprintf("use only compatible transports for kind=%q: %s", c.Kind, strings.Join(allowedNames, ", ")),
			))
		}
	}
	return results
}

// sliceMixesHTTPVisibility reports whether s serves at least one public
// (/api/*) HTTP contract and at least one internal (/internal/v1) HTTP
// contract via role=serve usages — the SLICE-HTTP-VISIBILITY-SEGREGATION-01
// violation condition.
func (v *Validator) sliceMixesHTTPVisibility(s *metadata.SliceMeta) bool {
	var hasPublic, hasInternal bool
	for _, cu := range s.ContractUsages {
		if cu.Role != string(cellvocab.RoleServe) {
			continue
		}
		c, ok := v.project.Contracts[cu.Contract]
		if !ok {
			continue // REF-05 covers dangling contract refs
		}
		if c.Kind != "http" || c.Endpoints.HTTP == nil {
			continue
		}
		switch path := c.Endpoints.HTTP.Path; {
		case metadata.IsInternalHTTPPath(path):
			hasInternal = true
		case metadata.IsPublicHTTPPath(path):
			hasPublic = true
		}
	}
	return hasPublic && hasInternal
}

// validateFMT35 enforces the per-role placement of the contractUsage
// handler / group / field / sourceID / targetSelector / projection / onReset
// columns. Each role permits a different subset of columns; the rule fires
// when a required column is absent or a forbidden column is set:
//
//   - subscribe:        handler required; group/field/projection/onReset optional; sourceID/targetSelector forbidden
//   - webhook-receive:  handler+sourceID required; field optional; group/targetSelector/projection/onReset forbidden
//   - webhook-dispatch: targetSelector+sourceID required; field optional; handler/group/projection/onReset forbidden
//   - serve (grpc):     field optional; all other columns forbidden
//   - serve (http):     all seven columns forbidden
//   - any other role:   all seven columns forbidden
//
// NOTE: the group-forbidden-when-projection-set coupling (a cross-column
// constraint) cannot be expressed in the per-role matrix; it is enforced by
// the parser (validateProjectionUniqueness) and the JSON schema instead.
//
// These constraints also live in slice.schema.json as if/then conditionals,
// but that schema is not run by `gocell validate` (see
// kernel/metadata/schemas/embed.go — "planned Phase 2"). FMT-35 is the live
// enforcement path, so the matrix below MUST stay in sync with the schema.
func (v *Validator) validateFMT35() []ValidationResult {
	var results []ValidationResult
	for _, s := range v.project.Slices {
		for i, cu := range s.ContractUsages {
			field := fmt.Sprintf("contractUsages[%d]", i)
			contractKind := v.contractKindFor(cu.Contract)
			results = append(results, v.checkFMT35Columns(s, cu, field, contractKind)...)
		}
	}
	return results
}

// contractKindFor returns the Kind string of the contract identified by id, or
// an empty string when the contract is not found (REF-05 covers dangling refs).
func (v *Validator) contractKindFor(id string) string {
	if c, ok := v.project.Contracts[id]; ok {
		return c.Kind
	}
	return ""
}

// fmt35Placement is the disposition of a single contractUsage placement column
// for one role.
type fmt35Placement uint8

const (
	fmt35Forbidden fmt35Placement = iota // column must be empty
	fmt35Optional                        // column may be empty or set
	fmt35Required                        // column must be non-empty
)

// fmt35RoleColumns returns the placement rule for each of the seven placement
// columns (handler, group, field, sourceID, targetSelector, projection, onReset)
// for the given role and contract kind. The matrix mirrors the if/then
// conditionals in slice.schema.json; any role/kind combination not listed
// forbids all seven columns.
//
// The contractKind parameter is used to distinguish grpc serve from http serve:
// a grpc serve CU may set the optional field column (for struct-field
// disambiguation when a cell owns multiple *sliceID.T fields), while http serve
// forbids all columns.
func fmt35RoleColumns(role, contractKind string) (handler, group, field, sourceID, targetSelector, projection, onReset fmt35Placement) {
	switch role {
	case string(cellvocab.RoleSubscribe):
		return fmt35Required, fmt35Optional, fmt35Optional, fmt35Forbidden, fmt35Forbidden, fmt35Optional, fmt35Optional
	case string(cellvocab.RoleWebhookReceive):
		return fmt35Required, fmt35Forbidden, fmt35Optional, fmt35Required, fmt35Forbidden, fmt35Forbidden, fmt35Forbidden
	case string(cellvocab.RoleWebhookDispatch):
		return fmt35Forbidden, fmt35Forbidden, fmt35Optional, fmt35Required, fmt35Required, fmt35Forbidden, fmt35Forbidden
	case string(cellvocab.RoleServe):
		if contractKind == string(cellvocab.ContractGRPC) {
			// grpc serve: field is optional for struct-field disambiguation;
			// all other placement columns are forbidden.
			return fmt35Forbidden, fmt35Forbidden, fmt35Optional, fmt35Forbidden, fmt35Forbidden, fmt35Forbidden, fmt35Forbidden
		}
		// http serve (and any other non-grpc serve): all columns forbidden.
		return fmt35Forbidden, fmt35Forbidden, fmt35Forbidden, fmt35Forbidden, fmt35Forbidden, fmt35Forbidden, fmt35Forbidden
	default:
		return fmt35Forbidden, fmt35Forbidden, fmt35Forbidden, fmt35Forbidden, fmt35Forbidden, fmt35Forbidden, fmt35Forbidden
	}
}

// checkFMT35Columns reports a FMT-35 finding for every placement column whose
// presence/absence violates the role's matrix entry.
func (v *Validator) checkFMT35Columns(
	s *metadata.SliceMeta, cu metadata.ContractUsage, field, contractKind string,
) []ValidationResult {
	hRule, gRule, fRule, sRule, tRule, projRule, resetRule := fmt35RoleColumns(cu.Role, contractKind)
	cols := []struct {
		name  string
		value string
		rule  fmt35Placement
	}{
		{"handler", cu.Handler, hRule},
		{"group", cu.Group, gRule},
		{"field", cu.Field, fRule},
		{"sourceID", cu.SourceID, sRule},
		{"targetSelector", cu.TargetSelector, tRule},
		{"projection", cu.Projection, projRule},
		{"onReset", cu.OnReset, resetRule},
	}
	var results []ValidationResult
	for _, c := range cols {
		switch {
		case c.rule == fmt35Required && c.value == "":
			results = append(results, v.fmt35RequiredColumn(s, cu, field, c.name))
		case c.rule == fmt35Forbidden && c.value != "":
			results = append(results, v.fmt35ForbiddenColumn(s, cu, field, c.name))
		}
	}
	return results
}

// fmt35RequiredColumn reports a FMT-35 error when a column required for the
// contractUsage's role is empty.
func (v *Validator) fmt35RequiredColumn(
	s *metadata.SliceMeta, cu metadata.ContractUsage, field, column string,
) ValidationResult {
	return v.newError(
		codeFMT35, IssueRequired,
		sliceFile(s), field+"."+column,
		fmt.Sprintf(
			"slice %q contractUsage %q has role=%q but no %s; %s is required for this role",
			s.ID, cu.Contract, cu.Role, column, column,
		),
		fmt.Sprintf("set %s: on this contractUsage (required for role=%q)", column, cu.Role),
	)
}

// fmt35ForbiddenColumn reports a FMT-35 error when a column forbidden for the
// contractUsage's role is set.
func (v *Validator) fmt35ForbiddenColumn(
	s *metadata.SliceMeta, cu metadata.ContractUsage, field, column string,
) ValidationResult {
	return v.newError(
		codeFMT35, IssueForbidden,
		sliceFile(s), field+"."+column,
		fmt.Sprintf(
			"slice %q contractUsage %q has role=%q but sets %s; %s is not valid for this role",
			s.ID, cu.Contract, cu.Role, column, column,
		),
		fmt.Sprintf("remove %s: from this contractUsage (not valid for role=%q)", column, cu.Role),
	)
}
