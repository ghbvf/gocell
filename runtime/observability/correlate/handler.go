package correlate

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
)

// correlateResponse is the top-level wire envelope for all successful
// responses from the correlate endpoint, matching the {data:{...}} shape
// required for single-resource responses per api-versioning.md.
type correlateResponse struct {
	Data correlateData `json:"data"`
}

// correlateData holds the payload. Exactly one mode's fields are populated.
// Trace mode populates Query, AuditEntries, HasMore, and Returned; cell mode
// populates Owner and Selectors.
type correlateData struct {
	// Trace mode
	Query        *queryInfo      `json:"query,omitempty"`
	AuditEntries []auditEntryDTO `json:"auditEntries,omitempty"`
	// HasMore is true when the trace has more than traceQueryLimit audit
	// entries; the returned set has been truncated. See TraceResult.HasMore.
	HasMore *bool `json:"hasMore,omitempty"`
	// Returned is the count of entries actually included in AuditEntries.
	Returned *int `json:"returned,omitempty"`

	// Cell mode
	Owner     *ownerDTO     `json:"owner,omitempty"`
	Selectors *selectorsDTO `json:"selectors,omitempty"`
}

// queryInfo echoes the lookup key so callers can confirm what was queried.
type queryInfo struct {
	TraceID string `json:"traceId"`
}

// HTTPHandler returns an http.Handler that serves the correlate endpoint.
//
// Exactly one of the query params traceId or cell must be present:
//   - both present  → 400
//   - neither       → 400
//   - only traceId  → trace mode
//   - only cell     → cell mode
//
// Each param value must be 1–256 bytes.
//
// Auth is enforced ahead of this handler: PrimaryListener JWT (listener level)
// plus an admin-role Policy (auth.AnyRole(auth.RoleAdmin), see routes.go). This
// handler performs no authentication or authorization itself.
func (s *Service) HTTPHandler() http.Handler {
	return http.HandlerFunc(s.serveHTTP)
}

func (s *Service) serveHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()

	// Use q.Has for key-presence detection: q.Get conflates absent (?key not
	// present) with present-but-empty (?key=). The XOR routing must treat
	// ?traceId= (present, empty) as 400 via validateParamLength, not silently
	// route to the other mode. After routing, validateParamLength still rejects
	// a present-but-empty value.
	hasTrace := q.Has("traceId")
	hasCell := q.Has("cell")

	switch {
	case hasTrace && hasCell:
		httputil.WriteError(ctx, w, errcode.New(
			errcode.KindInvalid,
			errcode.ErrValidationFailed,
			"exactly one of traceId or cell is required; both were provided",
			errcode.WithDetails(errcode.PublicString("field", "traceId,cell")),
		))
		return
	case !hasTrace && !hasCell:
		httputil.WriteError(ctx, w, errcode.New(
			errcode.KindInvalid,
			errcode.ErrValidationFailed,
			"exactly one of traceId or cell is required; neither was provided",
			errcode.WithDetails(errcode.PublicString("field", "traceId,cell")),
		))
		return
	case hasTrace:
		s.respondTrace(ctx, w, q.Get("traceId"))
	default:
		s.respondCell(ctx, w, q.Get("cell"))
	}
}

func (s *Service) respondTrace(ctx context.Context, w http.ResponseWriter, traceID string) {
	if err := validateParamLength(traceID, "traceId"); err != nil {
		httputil.WriteError(ctx, w, err)
		return
	}
	result, err := s.CorrelateByTrace(ctx, traceID)
	if err != nil {
		httputil.WriteError(ctx, w, err)
		return
	}
	hasMore := result.HasMore
	returned := result.Returned
	s.writeJSON(ctx, w, correlateResponse{
		Data: correlateData{
			Query:        &queryInfo{TraceID: result.TraceID},
			AuditEntries: result.AuditEntries,
			HasMore:      &hasMore,
			Returned:     &returned,
		},
	})
}

func (s *Service) respondCell(ctx context.Context, w http.ResponseWriter, cellID string) {
	if err := validateParamLength(cellID, "cell"); err != nil {
		httputil.WriteError(ctx, w, err)
		return
	}
	result, err := s.CorrelateByCell(ctx, cellID)
	if err != nil {
		httputil.WriteError(ctx, w, err)
		return
	}
	s.writeJSON(ctx, w, correlateResponse{
		Data: correlateData{
			Owner:     &result.Owner,
			Selectors: &result.Selectors,
		},
	})
}

// validateParamLength returns ErrValidationFailed when value is empty or
// longer than maxParamLen (256 bytes). Trace and cell IDs are ASCII opaque
// identifiers; byte-count semantics are correct and intentional.
func validateParamLength(value, paramName string) error {
	const maxParamLen = 256
	if len(value) == 0 {
		return errcode.New(
			errcode.KindInvalid, errcode.ErrValidationFailed,
			"query parameter must not be empty",
			errcode.WithDetails(errcode.PublicString("param", paramName)),
		)
	}
	if len(value) > maxParamLen {
		return errcode.New(
			errcode.KindInvalid, errcode.ErrValidationFailed,
			"query parameter exceeds maximum length",
			errcode.WithDetails(
				errcode.PublicString("param", paramName),
				errcode.PublicInt("maxLength", maxParamLen),
			),
		)
	}
	return nil
}

// writeJSON encodes v as JSON and writes it with status 200.
// Encoding errors are logged via the service logger with structured fields.
func (s *Service) writeJSON(ctx context.Context, w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.logger.ErrorContext(ctx, "correlate: write response", slog.Any("error", err))
	}
}
