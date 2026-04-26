package httputil

import "github.com/google/uuid"

// ParseCanonicalUUID parses raw as a UUID and returns the canonical lowercase
// dashed form when raw is in one of the two wire-allowed shapes:
//
//   - 36-char dashed: xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
//   - 32-char compact: xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
//
// Both case variants are accepted and normalized. Anything else returns
// ("", false), including:
//
//   - brace-wrapped Microsoft GUIDs ({xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx})
//   - urn:uuid:xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx
//   - whitespace padding (" xxx-xxx-... " happens to match length-38 brace
//     dispatch in google/uuid v1.6 and would otherwise be silently accepted)
//   - any other length, embedded whitespace, or stray punctuation
//
// google/uuid.Parse dispatches by string length and accepts all four extra
// forms above. ParseCanonicalUUID is the single chokepoint that holds GoCell
// to the OpenAPI 3.0 `format: uuid` convention used in contract.yaml
// pathParams: one shape on the wire, normalized once at the boundary.
//
// ref: ietf rfc 9562 §4 — uuid string representation; brace-wrapped and
// urn:uuid: forms are explicitly out of scope for the canonical text form.
func ParseCanonicalUUID(raw string) (string, bool) {
	if l := len(raw); l != 36 && l != 32 {
		return "", false
	}
	parsed, err := uuid.Parse(raw)
	if err != nil {
		return "", false
	}
	return parsed.String(), true
}
