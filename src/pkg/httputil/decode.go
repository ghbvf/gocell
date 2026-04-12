package httputil

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// DecodeJSON reads the request body as JSON into dst.
// The body must contain exactly one JSON value; trailing content is rejected.
// Unknown fields are silently ignored to maintain backward compatibility.
//
// Errors are returned as *errcode.Error:
//   - empty body           -> ErrValidationFailed, details: {"reason": "empty body"}
//   - truncated JSON       -> ErrValidationFailed, details: {"reason": "malformed JSON"}
//   - syntax error         -> ErrValidationFailed, details: {"reason": "malformed JSON", ...}
//   - type mismatch        -> ErrValidationFailed, details: {"reason": "type mismatch", "field": ...}
//   - trailing content     -> ErrValidationFailed, details: {"reason": "trailing content after JSON value"}
//   - body too large       -> ErrBodyTooLarge
//   - other                -> ErrInternal (details not exposed)
func DecodeJSON(r *http.Request, dst any) error {
	return decodeJSON(r, dst, false)
}

// DecodeJSONStrict is like DecodeJSON but rejects unknown fields.
// When the destination is a struct, any JSON key that does not match
// a non-ignored exported field causes a 400 error with details:
//
//	{"reason": "unknown field", "field": "<name>"}
//
// Map destinations are unaffected — they accept any key regardless.
func DecodeJSONStrict(r *http.Request, dst any) error {
	return decodeJSON(r, dst, true)
}

func decodeJSON(r *http.Request, dst any, strict bool) error {
	dec := json.NewDecoder(r.Body)
	if strict {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(dst); err != nil {
		return classifyDecodeError(err)
	}
	// Reject trailing content: a second Decode must return io.EOF.
	// dec.More() is insufficient — it returns false for stray '}' and ']',
	// letting invalid input like `{"name":"ok"}}` pass silently.
	if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		// The second decode may hit the body size limit instead of trailing
		// content — surface that as 413, not 400.
		if isMaxBytesError(err) {
			return errcode.New(errcode.ErrBodyTooLarge, "request body too large")
		}
		return errcode.WithDetails(
			errcode.New(errcode.ErrValidationFailed, "invalid request body"),
			map[string]any{"reason": "trailing content after JSON value"},
		)
	}
	return nil
}

func classifyDecodeError(err error) *errcode.Error {
	switch {
	case errors.Is(err, io.EOF):
		return errcode.WithDetails(
			errcode.New(errcode.ErrValidationFailed, "invalid request body"),
			map[string]any{"reason": "empty body"},
		)
	case errors.Is(err, io.ErrUnexpectedEOF):
		return errcode.WithDetails(
			errcode.New(errcode.ErrValidationFailed, "invalid request body"),
			map[string]any{"reason": "malformed JSON"},
		)
	case isMaxBytesError(err):
		return errcode.New(errcode.ErrBodyTooLarge, "request body too large")
	default:
		var syntaxErr *json.SyntaxError
		if errors.As(err, &syntaxErr) {
			return errcode.WithDetails(
				errcode.New(errcode.ErrValidationFailed, "invalid request body"),
				map[string]any{"reason": "malformed JSON", "offset": syntaxErr.Offset},
			)
		}
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			return errcode.WithDetails(
				errcode.New(errcode.ErrValidationFailed, "invalid request body"),
				map[string]any{"reason": "type mismatch", "field": typeErr.Field},
			)
		}
		// DisallowUnknownFields produces: json: unknown field "fieldName"
		if msg := err.Error(); strings.HasPrefix(msg, "json: unknown field") {
			field := strings.TrimPrefix(msg, `json: unknown field `)
			field = strings.Trim(field, `"`)
			return errcode.WithDetails(
				errcode.New(errcode.ErrValidationFailed, "invalid request body"),
				map[string]any{"reason": "unknown field", "field": field},
			)
		}
		return errcode.Wrap(errcode.ErrInternal, "internal server error", err)
	}
}

func isMaxBytesError(err error) bool {
	var maxBytesErr *http.MaxBytesError
	return errors.As(err, &maxBytesErr)
}
