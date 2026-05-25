package governance

import (
	"context"
	"fmt"
	"sort"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// sortedContracts returns all contracts from v.project in ascending ID order.
// It is the canonical contract list for PhaseHealth rules so that
// checkCH01–checkCH06 iterate deterministically without accepting a caller-
// supplied slice.
func (v *Validator) sortedContracts() []*metadata.ContractMeta {
	ids := make([]string, 0, len(v.project.Contracts))
	for id := range v.project.Contracts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]*metadata.ContractMeta, 0, len(ids))
	for _, id := range ids {
		out = append(out, v.project.Contracts[id])
	}
	return out
}

// CheckHealth runs all PhaseHealth rules (CH-01 through CH-06) against the
// project loaded into v and returns the combined findings.
// It is the single entry point for `gocell check contract-health`.
func (v *Validator) CheckHealth(ctx context.Context) []ValidationResult {
	rules := rulesForPhases(PhaseHealth)
	results, _ := v.run(ctx, rules, false)
	return results
}

// checkCH01 verifies that every contract declares an ownerCell.
func (v *Validator) checkCH01() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.sortedContracts() {
		if c.OwnerCell == "" {
			results = append(results, v.newError(
				codeCH01, IssueRequired,
				c.File, "ownerCell",
				fmt.Sprintf("%s: missing ownerCell", c.ID),
				"set ownerCell to the cell id that owns this contract",
			))
		}
	}
	return results
}

// checkCH02 verifies that every contract declares a lifecycle.
func (v *Validator) checkCH02() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.sortedContracts() {
		if c.Lifecycle == "" {
			results = append(results, v.newError(
				codeCH02, IssueRequired,
				c.File, "lifecycle",
				fmt.Sprintf("%s: missing lifecycle", c.ID),
				"set lifecycle to draft, active, or deprecated",
			))
		}
	}
	return results
}

// checkCH03 verifies that HTTP contracts declare complete schemaRefs.
func (v *Validator) checkCH03() []ValidationResult {
	var results []ValidationResult
	for _, c := range v.sortedContracts() {
		if c.Kind == "http" {
			results = append(results, v.checkHTTPSchemaRefs(c)...)
		}
	}
	return results
}

// checkHTTPSchemaRefs enforces schemaRefs completeness for HTTP contracts.
//   - noContent endpoints (typically DELETE/204) skip schema checks entirely
//   - non-noContent endpoints need a response schemaRef
//   - PUT/PATCH need a request schemaRef
//   - every declared responses[N] entry needs a non-empty schemaRef
func (v *Validator) checkHTTPSchemaRefs(c *metadata.ContractMeta) []ValidationResult {
	if c.Endpoints.HTTP != nil && c.Endpoints.HTTP.NoContent {
		return nil
	}

	if c.SchemaRefs.Request == "" && c.SchemaRefs.Response == "" {
		return []ValidationResult{v.newError(
			codeCH03, IssueRequired,
			c.File, "schemaRefs",
			fmt.Sprintf("%s: HTTP contract missing schemaRefs", c.ID),
			"add schemaRefs.request and schemaRefs.response pointing to JSON schema files",
		)}
	}

	var results []ValidationResult

	if c.SchemaRefs.Response == "" {
		results = append(results, v.newError(
			codeCH03, IssueRequired,
			c.File, "schemaRefs.response",
			fmt.Sprintf("%s: HTTP contract missing response schemaRefs", c.ID),
			"add schemaRefs.response pointing to a JSON schema file",
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
		return []ValidationResult{v.newError(
			codeCH03, IssueRequired,
			c.File, "schemaRefs.request",
			fmt.Sprintf("%s: %s contract missing request schemaRefs", c.ID, method),
			"add schemaRefs.request pointing to a JSON schema file",
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
			results = append(results, v.newError(
				codeCH03, IssueRequired,
				c.File, fmt.Sprintf("endpoints.http.responses[%d].schemaRef", status),
				fmt.Sprintf("%s: responses[%d] declared but missing schemaRef", c.ID, status),
				fmt.Sprintf("add schemaRef pointing to a JSON schema file for status %d", status),
			))
		}
	}
	return results
}
