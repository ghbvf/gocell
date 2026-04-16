package governance

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/metadata"
)

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
			results = append(results, ValidationResult{
				Code:      "FMT-01",
				Severity:  SeverityError,
				IssueType: IssueInvalid,
				File:      contractFile(c.ID),
				Field:     "lifecycle",
				Message:   fmt.Sprintf("contract %q lifecycle %q is not valid (must be draft, active, or deprecated)", c.ID, c.Lifecycle),
			})
		}
	}
	return results
}

// validateFMT02 checks that cell.type is one of {core, edge, support}.
func (v *Validator) validateFMT02() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Cells {
		if !validCellTypes[c.Type] {
			results = append(results, ValidationResult{
				Code:      "FMT-02",
				Severity:  SeverityError,
				IssueType: IssueInvalid,
				File:      cellFile(c.ID),
				Field:     "type",
				Message:   fmt.Sprintf("cell %q type %q is not valid (must be core, edge, or support)", c.ID, c.Type),
			})
		}
	}
	return results
}

// validateFMT03 checks that consistencyLevel is valid (L0-L4) for both cells and contracts.
func (v *Validator) validateFMT03() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Cells {
		if _, err := cell.ParseLevel(c.ConsistencyLevel); err != nil {
			results = append(results, ValidationResult{
				Code:      "FMT-03",
				Severity:  SeverityError,
				IssueType: IssueInvalid,
				File:      cellFile(c.ID),
				Field:     "consistencyLevel",
				Message:   fmt.Sprintf("cell %q consistencyLevel %q is not valid (must be L0-L4)", c.ID, c.ConsistencyLevel),
			})
		}
	}
	for _, c := range v.project.Contracts {
		if _, err := cell.ParseLevel(c.ConsistencyLevel); err != nil {
			results = append(results, ValidationResult{
				Code:      "FMT-03",
				Severity:  SeverityError,
				IssueType: IssueInvalid,
				File:      contractFile(c.ID),
				Field:     "consistencyLevel",
				Message:   fmt.Sprintf("contract %q consistencyLevel %q is not valid (must be L0-L4)", c.ID, c.ConsistencyLevel),
			})
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
				results = append(results, ValidationResult{
					Code:      "FMT-04",
					Severity:  SeverityError,
					IssueType: IssueRequired,
					File:      contractFile(c.ID),
					Field:     "replayable",
					Message:   fmt.Sprintf("%s contract %q must specify replayable", c.Kind, c.ID),
				})
			}
		}

		// Only event contracts require idempotencyKey and deliverySemantics.
		if kind == cell.ContractEvent {
			if c.IdempotencyKey == "" {
				results = append(results, ValidationResult{
					Code:      "FMT-04",
					Severity:  SeverityError,
					IssueType: IssueRequired,
					File:      contractFile(c.ID),
					Field:     "idempotencyKey",
					Message:   fmt.Sprintf("event contract %q must specify idempotencyKey", c.ID),
				})
			}
			if c.DeliverySemantics == "" {
				results = append(results, ValidationResult{
					Code:      "FMT-04",
					Severity:  SeverityError,
					IssueType: IssueRequired,
					File:      contractFile(c.ID),
					Field:     "deliverySemantics",
					Message:   fmt.Sprintf("event contract %q must specify deliverySemantics", c.ID),
				})
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
				results = append(results, ValidationResult{
					Code:      "FMT-05",
					Severity:  SeverityError,
					IssueType: IssueInvalid,
					File:      sliceFile(key),
					Field:     fmt.Sprintf("contractUsages[%d].role", i),
					Message:   fmt.Sprintf("role %q is not a valid contract role", cu.Role),
				})
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
			results = append(results, ValidationResult{
				Code:      "FMT-06",
				Severity:  SeverityError,
				IssueType: IssueRequired,
				File:      cellFile(c.ID),
				Field:     "schema.primary",
				Message:   fmt.Sprintf("non-L0 cell %q must have schema.primary", c.ID),
			})
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
			results = append(results, ValidationResult{
				Code:      "FMT-07",
				Severity:  SeverityError,
				IssueType: IssueRequired,
				File:      contractFile(c.ID),
				Field:     field,
				Message:   fmt.Sprintf("contract %q (kind %q) must have a provider endpoint", c.ID, c.Kind),
			})
		}
	}
	return results
}

// validateFMT09 checks that contract.kind is one of {http, event, command, projection}.
func (v *Validator) validateFMT09() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if !validKinds[c.Kind] {
			results = append(results, ValidationResult{
				Code:      "FMT-09",
				Severity:  SeverityError,
				IssueType: IssueInvalid,
				File:      contractFile(c.ID),
				Field:     "kind",
				Message:   fmt.Sprintf("contract %q kind %q is not valid (must be http, event, command, or projection)", c.ID, c.Kind),
			})
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
			results = append(results, ValidationResult{
				Code:      "FMT-10",
				Severity:  SeverityError,
				IssueType: IssueForbidden,
				File:      cellFile(c.ID),
				Field:     "id",
				Message:   fmt.Sprintf("cell ID %q is a banned legacy field name; use %q instead", c.ID, replacement),
			})
		}
	}

	// Check contract IDs for slash-separated format (should be dot-separated).
	for _, c := range v.project.Contracts {
		if strings.Contains(c.ID, "/") {
			results = append(results, ValidationResult{
				Code:      "FMT-10",
				Severity:  SeverityError,
				IssueType: IssueInvalid,
				File:      contractFile(c.ID),
				Field:     "id",
				Message:   fmt.Sprintf("contract ID %q uses slash separator; must use dot-separated format (e.g., kind.domain.version)", c.ID),
			})
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
			results = append(results, ValidationResult{
				Code:      "FMT-08",
				Severity:  SeverityError,
				IssueType: IssueInvalid,
				File:      contractFile(c.ID),
				Field:     "id",
				Message:   fmt.Sprintf("contract ID %q format is invalid (missing '.' separator)", c.ID),
			})
			continue
		}
		prefix := parts[0]
		if prefix != c.Kind {
			results = append(results, ValidationResult{
				Code:      "FMT-08",
				Severity:  SeverityError,
				IssueType: IssueMismatch,
				File:      contractFile(c.ID),
				Field:     "kind",
				Message:   fmt.Sprintf("contract %q ID prefix %q does not match kind %q", c.ID, prefix, c.Kind),
			})
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
			results = append(results, ValidationResult{
				Code:      "FMT-11",
				Severity:  SeverityError,
				IssueType: IssueRequired,
				File:      cellFile(c.ID),
				Field:     "owner.team",
				Message:   fmt.Sprintf("cell %q must have owner.team", c.ID),
			})
		}
		if c.Owner.Role == "" {
			results = append(results, ValidationResult{
				Code:      "FMT-11",
				Severity:  SeverityError,
				IssueType: IssueRequired,
				File:      cellFile(c.ID),
				Field:     "owner.role",
				Message:   fmt.Sprintf("cell %q must have owner.role", c.ID),
			})
		}
		if len(c.Verify.Smoke) == 0 {
			results = append(results, ValidationResult{
				Code:      "FMT-11",
				Severity:  SeverityError,
				IssueType: IssueRequired,
				File:      cellFile(c.ID),
				Field:     "verify.smoke",
				Message:   fmt.Sprintf("cell %q must have at least one verify.smoke entry", c.ID),
			})
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
			results = append(results, ValidationResult{
				Code:      "FMT-12",
				Severity:  SeverityError,
				IssueType: IssueRequired,
				File:      sliceFile(key),
				Field:     "verify.unit",
				Message:   fmt.Sprintf("slice %q must have at least one verify.unit entry", s.ID),
			})
		}
	}
	return results
}

// codeFMT13 is the rule code for HTTP transport metadata validation.
const codeFMT13 = "FMT-13"

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
		return []ValidationResult{{
			Code: codeFMT13, Severity: SeverityError, IssueType: IssueInvalid,
			File: file, Field: "endpoints.http",
			Message: fmt.Sprintf("contract %q can only declare endpoints.http when kind is http", c.ID),
		}}
	}

	var results []ValidationResult
	results = append(results, v.validateFMT13Method(c, httpMeta, file)...)
	results = append(results, v.validateFMT13Path(c, httpMeta, file)...)
	results = append(results, v.validateFMT13Status(c, httpMeta, file)...)
	results = append(results, v.validateFMT13NoContent(c, httpMeta, file)...)
	return results
}

func (v *Validator) validateFMT13Method(c *metadata.ContractMeta, h *metadata.HTTPTransportMeta, file string) []ValidationResult {
	if h.Method == "" {
		return []ValidationResult{{
			Code: codeFMT13, Severity: SeverityError, IssueType: IssueRequired,
			File: file, Field: "endpoints.http.method",
			Message: fmt.Sprintf("http contract %q must specify endpoints.http.method once endpoints.http is present", c.ID),
		}}
	}
	if !validHTTPMethods[strings.ToUpper(h.Method)] {
		return []ValidationResult{{
			Code: codeFMT13, Severity: SeverityError, IssueType: IssueInvalid,
			File: file, Field: "endpoints.http.method",
			Message: fmt.Sprintf("http contract %q method %q is not supported", c.ID, h.Method),
		}}
	}
	return nil
}

func (v *Validator) validateFMT13Path(c *metadata.ContractMeta, h *metadata.HTTPTransportMeta, file string) []ValidationResult {
	if h.Path == "" {
		return []ValidationResult{{
			Code: codeFMT13, Severity: SeverityError, IssueType: IssueRequired,
			File: file, Field: "endpoints.http.path",
			Message: fmt.Sprintf("http contract %q must specify endpoints.http.path once endpoints.http is present", c.ID),
		}}
	}
	if !strings.HasPrefix(h.Path, "/") {
		return []ValidationResult{{
			Code: codeFMT13, Severity: SeverityError, IssueType: IssueInvalid,
			File: file, Field: "endpoints.http.path",
			Message: fmt.Sprintf("http contract %q path %q must start with '/'", c.ID, h.Path),
		}}
	}
	return nil
}

func (v *Validator) validateFMT13Status(c *metadata.ContractMeta, h *metadata.HTTPTransportMeta, file string) []ValidationResult {
	if h.SuccessStatus == 0 {
		return []ValidationResult{{
			Code: codeFMT13, Severity: SeverityError, IssueType: IssueRequired,
			File: file, Field: "endpoints.http.successStatus",
			Message: fmt.Sprintf("http contract %q must specify endpoints.http.successStatus once endpoints.http is present", c.ID),
		}}
	}
	if h.SuccessStatus < 200 || h.SuccessStatus > 299 {
		return []ValidationResult{{
			Code: codeFMT13, Severity: SeverityError, IssueType: IssueInvalid,
			File: file, Field: "endpoints.http.successStatus",
			Message: fmt.Sprintf("http contract %q successStatus %d must be a 2xx code", c.ID, h.SuccessStatus),
		}}
	}
	return nil
}

func (v *Validator) validateFMT13NoContent(c *metadata.ContractMeta, h *metadata.HTTPTransportMeta, file string) []ValidationResult {
	var results []ValidationResult

	if h.NoContent {
		if h.SuccessStatus != 0 && h.SuccessStatus != 204 {
			results = append(results, ValidationResult{
				Code: codeFMT13, Severity: SeverityError, IssueType: IssueMismatch,
				File: file, Field: "endpoints.http.noContent",
				Message: fmt.Sprintf("http contract %q with noContent=true must use successStatus 204", c.ID),
			})
		}
		if c.SchemaRefs.Response != "" {
			results = append(results, ValidationResult{
				Code: codeFMT13, Severity: SeverityError, IssueType: IssueForbidden,
				File: file, Field: "schemaRefs.response",
				Message: fmt.Sprintf("http contract %q with noContent=true must not declare schemaRefs.response", c.ID),
			})
		}
	} else if h.SuccessStatus == 204 {
		results = append(results, ValidationResult{
			Code: codeFMT13, Severity: SeverityError, IssueType: IssueMismatch,
			File: file, Field: "endpoints.http.noContent",
			Message: fmt.Sprintf("http contract %q with successStatus 204 must set noContent=true", c.ID),
		})
	}

	// Advisory: noContent=false without schemaRefs.response is likely incomplete.
	if !h.NoContent && c.SchemaRefs.Response == "" {
		results = append(results, ValidationResult{
			Code: codeFMT13, Severity: SeverityWarning, IssueType: IssueRequired,
			File: file, Field: "schemaRefs.response",
			Message: fmt.Sprintf("http contract %q with noContent=false should declare schemaRefs.response", c.ID),
		})
	}

	return results
}

// validateFMT14 checks that every slice declares explicit allowedFiles.
func (v *Validator) validateFMT14() []ValidationResult {
	var results []ValidationResult
	for key, s := range v.project.Slices {
		if len(s.AllowedFiles) == 0 {
			results = append(results, ValidationResult{
				Code:      "FMT-14",
				Severity:  SeverityError,
				IssueType: IssueRequired,
				File:      sliceFile(key),
				Field:     "allowedFiles",
				Message:   fmt.Sprintf("slice %q must declare explicit allowedFiles", s.ID),
			})
		}
	}
	return results
}

// validateFMT15 checks that HTTP list-style response schemas include "hasMore"
// in their required fields. A response is a "list" when properties.data.type is "array".
// Skipped when root is empty, for non-HTTP contracts, or when the schema file cannot be read.
func (v *Validator) validateFMT15() []ValidationResult {
	if v.root == "" {
		return nil
	}
	var results []ValidationResult
	for _, c := range v.project.Contracts {
		if c.Kind != "http" || c.SchemaRefs.Response == "" {
			continue
		}
		contractDir := filepath.Join(v.root, contractDirFromID(c.ID))
		schemaPath := filepath.Join(contractDir, c.SchemaRefs.Response)
		if !isWithinRoot(v.root, schemaPath) {
			continue
		}
		data, err := v.readFile(schemaPath)
		if err != nil {
			continue // REF-12 handles missing files
		}
		if !isListSchema(data) {
			continue
		}
		if !hasMoreInRequired(data) {
			results = append(results, ValidationResult{
				Code:      "FMT-15",
				Severity:  SeverityError,
				IssueType: IssueRequired,
				File:      contractFile(c.ID),
				Field:     "schemaRefs.response",
				Message:   fmt.Sprintf("list response schema for contract %q must include \"hasMore\" in required fields", c.ID),
			})
		}
	}
	return results
}

// isListSchema checks if a JSON schema has properties.data.type == "array".
func isListSchema(data []byte) bool {
	var schema struct {
		Properties struct {
			Data struct {
				Type string `json:"type"`
			} `json:"data"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		return false
	}
	return schema.Properties.Data.Type == "array"
}

// hasMoreInRequired checks if "hasMore" is in the JSON schema required array.
func hasMoreInRequired(data []byte) bool {
	var schema struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(data, &schema); err != nil {
		return false
	}
	for _, r := range schema.Required {
		if r == "hasMore" {
			return true
		}
	}
	return false
}
