package contractgen

import (
	"fmt"
)

// CREDENTIAL-RESPONSE-IDEMPOTENCY-EXEMPT-FUNNEL-01 — invariant statement and
// AI-robust grading live in the archtest package godoc (single source of truth):
// tools/archtest/credential_response_idempotency_funnel_test.go.
// Behavioral coverage lives in credential_response_idempotency_guard_test.go.
//
// Summary: for any kind:http contract whose response schema declares a sensitive
// key (per pkg/redaction.IsSensitiveKey) at any depth, the contract MUST set
// endpoints.http.idempotency.exempt: true. Without the exemption the HTTP
// idempotency store would record+replay a credential-bearing response into Redis
// (24h TTL), persisting live tokens that must never be replay-eligible (issue
// #1469). This guard rejects the schema at generation time so the leaking handler
// can never be produced.
//
// Symmetric to AUDIT-WIRE-SENSITIVE-FIELD-FUNNEL-01 (which protects audit-domain
// wire-out from projecting Principal credentials). The shared tree-walk is
// walkSchemaSensitiveKeys in sensitive_wire_guard.go.

// rejectUnexemptCredentialResponse returns an error when resp declares a
// property whose name matches pkg/redaction.IsSensitiveKey at any nesting depth
// AND idempotencyExempt is false. When idempotencyExempt is true the route has
// already opted out of the idempotency store — no recording occurs, so any
// response field is safe. A nil resp (e.g. 204 noContent) is always safe.
//
// Error messages name the contract, the offending field path, and the fix
// ("set endpoints.http.idempotency.exempt: true") so authors can act
// immediately without consulting documentation.
func rejectUnexemptCredentialResponse(contractID string, idempotencyExempt bool, resp *Schema) error {
	if idempotencyExempt {
		return nil // route opted out of idempotency store — safe
	}
	var firstErr error
	walkSchemaSensitiveKeys(resp, "$", func(fieldPath, key string) {
		if firstErr != nil {
			return
		}
		firstErr = fmt.Errorf(
			"contract %q response schema declares sensitive-key field %q at %s: "+
				"this response is eligible for idempotency-store recording (Redis, 24h TTL), "+
				"which would persist live credentials — "+
				"set endpoints.http.idempotency.exempt: true to opt this route out "+
				"(CREDENTIAL-RESPONSE-IDEMPOTENCY-EXEMPT-FUNNEL-01)",
			contractID, key, fieldPath)
	})
	return firstErr
}
