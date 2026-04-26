package httputil

import (
	"fmt"
	"net/http"

	"github.com/google/uuid"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// ParseUUIDPathParam extracts a UUID-typed path parameter from r and writes a
// 400 ERR_VALIDATION_INVALID_UUID response if the value is missing or
// malformed. On success it returns the canonical lowercase UUID string and
// ok=true; on failure it returns ("", false) after the response has been
// written, and the caller MUST return immediately.
//
// The contract.yaml convention `pathParams.{name}.format: uuid` is the
// authoritative discriminator: any handler serving such a path must use this
// helper at the entry point. The CH-05 governance rule enforces that link
// statically.
func ParseUUIDPathParam(w http.ResponseWriter, r *http.Request, name string) (string, bool) {
	raw := r.PathValue(name)
	parsed, err := uuid.Parse(raw)
	if err != nil {
		WriteError(r.Context(), w, http.StatusBadRequest,
			string(errcode.ErrValidationInvalidUUID),
			fmt.Sprintf("path parameter %q must be a valid UUID", name))
		return "", false
	}
	return parsed.String(), true
}
