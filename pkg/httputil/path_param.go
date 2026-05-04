package httputil

import (
	"fmt"
	"net/http"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// ParseUUIDPathParam extracts a UUID-typed path parameter from r and writes a
// 400 ERR_VALIDATION_INVALID_UUID response if the value is missing or
// malformed. On success it returns the canonical lowercase dashed UUID string
// and ok=true; on failure it returns ("", false) after the response has been
// written, and the caller MUST return immediately.
//
// Accepts both canonical dashed (xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx) and
// compact 32-char hex forms; both are normalized to canonical lowercase dashed
// UUID before being returned. Brace-wrapped {xxx}, urn:uuid:xxx, whitespace
// padding, and any other shape are rejected with 400 — the helper delegates
// to ParseCanonicalUUID, which is stricter than google/uuid.Parse on purpose
// (see uuidcanonical.go).
//
// name MUST match the `pathParams.{name}` key declared in contract.yaml; the
// same name appears verbatim in the 400 response message so clients can
// identify the offending parameter.
//
// The contract.yaml convention `pathParams.{name}.format: uuid` is the
// authoritative discriminator: any handler serving such a path must use this
// helper at the entry point. The CH-05 governance rule enforces that link
// statically.
func ParseUUIDPathParam(w http.ResponseWriter, r *http.Request, name string) (string, bool) {
	canonical, ok := ParseCanonicalUUID(r.PathValue(name))
	if !ok {
		WriteError(r.Context(), w, errcode.New(
			errcode.KindInvalid,
			errcode.ErrValidationInvalidUUID,
			fmt.Sprintf("path parameter %q must be a valid UUID", name),
		))
		return "", false
	}
	return canonical, true
}
