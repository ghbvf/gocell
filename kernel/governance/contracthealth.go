package governance

import (
	"fmt"
	"sort"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// CH-XX rule codes — kept distinct from FMT/REF/TOPO so it's clear at a
// glance whether a finding came from `gocell validate` (governance rules)
// or `gocell check contract-health` (CI-blocking contract metadata
// invariants).
const (
	CodeContractHealthOwner     = "CH-01"
	CodeContractHealthLifecycle = "CH-02"
	CodeContractHealthSchema    = "CH-03"
)

// CheckContractHealth runs CI-blocking contract metadata invariants:
//
//   - ownerCell must be set
//   - lifecycle must be set
//   - HTTP contracts must declare schemaRefs (request + response unless
//     noContent; PUT/PATCH always need request schema; declared
//     responses[N] entries each need a schemaRef)
//
// Findings reuse Validator's locator so Line/Column resolve to yaml.Node
// field-level positions — same precision as `gocell validate` rules.
func (v *Validator) CheckContractHealth(contracts []*metadata.ContractMeta) []ValidationResult {
	var results []ValidationResult
	for _, c := range contracts {
		if c.OwnerCell == "" {
			results = append(results, v.newResult(
				CodeContractHealthOwner, SeverityError, IssueRequired,
				c.File, "ownerCell",
				fmt.Sprintf("%s: missing ownerCell", c.ID),
			))
		}
		if c.Lifecycle == "" {
			results = append(results, v.newResult(
				CodeContractHealthLifecycle, SeverityError, IssueRequired,
				c.File, "lifecycle",
				fmt.Sprintf("%s: missing lifecycle", c.ID),
			))
		}
		if c.Kind == "http" {
			results = append(results, v.checkHTTPSchemaRefs(c)...)
		}
	}
	return results
}

// checkHTTPSchemaRefs enforces schemaRefs completeness for HTTP contracts.
// Logic mirrors what was previously in cmd/gocell/app/check.go:
//   - noContent endpoints (typically DELETE/204) skip schema checks entirely
//   - non-noContent endpoints need a response schemaRef
//   - PUT/PATCH need a request schemaRef
//   - every declared responses[N] entry needs a non-empty schemaRef
func (v *Validator) checkHTTPSchemaRefs(c *metadata.ContractMeta) []ValidationResult {
	if c.Endpoints.HTTP != nil && c.Endpoints.HTTP.NoContent {
		return nil
	}

	if c.SchemaRefs.Request == "" && c.SchemaRefs.Response == "" {
		return []ValidationResult{v.newResult(
			CodeContractHealthSchema, SeverityError, IssueRequired,
			c.File, "schemaRefs",
			fmt.Sprintf("%s: HTTP contract missing schemaRefs", c.ID),
		)}
	}

	var results []ValidationResult

	if c.SchemaRefs.Response == "" {
		results = append(results, v.newResult(
			CodeContractHealthSchema, SeverityError, IssueRequired,
			c.File, "schemaRefs.response",
			fmt.Sprintf("%s: HTTP contract missing response schemaRefs", c.ID),
		))
	}

	if c.Endpoints.HTTP != nil {
		results = append(results, v.checkHTTPMethodSchema(c)...)
		results = append(results, v.checkHTTPResponseEntries(c)...)
	}

	return results
}

// checkHTTPMethodSchema checks that PUT/PATCH contracts declare a request schema.
func (v *Validator) checkHTTPMethodSchema(c *metadata.ContractMeta) []ValidationResult {
	method := c.Endpoints.HTTP.Method
	if (method == "PUT" || method == "PATCH") && c.SchemaRefs.Request == "" {
		return []ValidationResult{v.newResult(
			CodeContractHealthSchema, SeverityError, IssueRequired,
			c.File, "schemaRefs.request",
			fmt.Sprintf("%s: %s contract missing request schemaRefs", c.ID, method),
		)}
	}
	return nil
}

// checkHTTPResponseEntries verifies that every declared responses[N] entry
// carries a non-empty schemaRef. Iterates in ascending status-code order for
// deterministic output across map iteration.
func (v *Validator) checkHTTPResponseEntries(c *metadata.ContractMeta) []ValidationResult {
	statuses := make([]int, 0, len(c.Endpoints.HTTP.Responses))
	for status := range c.Endpoints.HTTP.Responses {
		statuses = append(statuses, status)
	}
	sort.Ints(statuses)

	var results []ValidationResult
	for _, status := range statuses {
		resp := c.Endpoints.HTTP.Responses[status]
		if resp.SchemaRef == "" {
			results = append(results, v.newResult(
				CodeContractHealthSchema, SeverityError, IssueRequired,
				c.File, fmt.Sprintf("endpoints.http.responses[%d].schemaRef", status),
				fmt.Sprintf("%s: responses[%d] declared but missing schemaRef", c.ID, status),
			))
		}
	}
	return results
}
