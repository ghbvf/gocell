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
// It enables DisallowUnknownFields by default (only affects struct targets;
// map targets accept any key regardless).
//
// Errors are returned as *errcode.Error:
//   - empty body           -> ErrValidationFailed, details: {"reason": "empty body"}
//   - truncated JSON       -> ErrValidationFailed, details: {"reason": "malformed JSON"}
//   - syntax error         -> ErrValidationFailed, details: {"reason": "malformed JSON", ...}
//   - type mismatch        -> ErrValidationFailed, details: {"reason": "type mismatch", "field": ...}
//   - unknown field        -> ErrValidationFailed, details: {"reason": "unknown field", "field": ...}
//   - trailing content     -> ErrValidationFailed, details: {"reason": "trailing content after JSON value"}
//   - body too large       -> ErrBodyTooLarge
//   - other                -> ErrInternal (details not exposed)
func DecodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return classifyDecodeError(err)
	}
	// Reject trailing content: body must contain exactly one JSON value.
	if dec.More() {
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
		// DisallowUnknownFields produces a plain error whose message starts with
		// "json: unknown field". This is not a public API guarantee — verify on
		// Go version upgrades. See https://github.com/golang/go/issues/29035.
		if strings.HasPrefix(err.Error(), "json: unknown field") {
			field := strings.TrimPrefix(err.Error(), "json: unknown field ")
			field = strings.Trim(field, "\"")
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
