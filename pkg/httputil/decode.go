package httputil

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"

	"github.com/ghbvf/gocell/pkg/errcode"
)

const msgInvalidRequestBody = "invalid request body"

// DefaultDecodeJSONLimit is the default maximum JSON request size (1 MiB).
const DefaultDecodeJSONLimit int64 = 1 << 20

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
func DecodeJSON(r *http.Request, dst any, maxBytes int64) error {
	return decodeJSON(r, dst, maxBytes, false)
}

// DecodeJSONStrict is like DecodeJSON but rejects unknown fields.
// All errors documented on DecodeJSON apply, plus the unknown field error:
//
//   - unknown field → ErrValidationFailed, details: {"reason": "unknown field", "field": ...}
//
// When the destination is a struct, any JSON key that does not match
// a non-ignored exported field causes a 400 error.
// Map destinations are unaffected — they accept any key regardless.
func DecodeJSONStrict(r *http.Request, dst any, maxBytes int64) error {
	return decodeJSON(r, dst, maxBytes, true)
}

func decodeJSON(r *http.Request, dst any, maxBytes int64, strict bool) error {
	body, err := readLimitedJSONBody(r, maxBytes)
	if err != nil {
		return err
	}
	if err := validateSingleJSONValue(body); err != nil {
		return err
	}
	if strict {
		if err := rejectUnknownJSONFields(body, dst); err != nil {
			return err
		}
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return classifyDecodeError(err)
	}
	return nil
}

func readLimitedJSONBody(r *http.Request, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultDecodeJSONLimit
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBytes+1))
	if err != nil {
		if isMaxBytesError(err) {
			return nil, bodyTooLargeError()
		}
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "internal server error", err,
			errcode.WithCategory(errcode.CategoryInfra))
	}
	if int64(len(body)) > maxBytes {
		return nil, bodyTooLargeError()
	}
	return body, nil
}

func validateSingleJSONValue(body []byte) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(new(json.RawMessage)); err != nil {
		return classifyDecodeError(err)
	}
	// Reject trailing content: a second Decode must return io.EOF.
	// dec.More() is insufficient — it returns false for stray '}' and ']',
	// letting invalid input like `{"name":"ok"}}` pass silently.
	if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return validationFailedWithDetails(map[string]any{"reason": "trailing content after JSON value"})
	}
	return nil
}

func classifyDecodeError(err error) error {
	switch {
	case errors.Is(err, io.EOF):
		return validationFailedWithDetails(map[string]any{"reason": "empty body"})
	case errors.Is(err, io.ErrUnexpectedEOF):
		return validationFailedWithDetails(map[string]any{"reason": "malformed JSON"})
	case isMaxBytesError(err):
		return bodyTooLargeError()
	default:
		var syntaxErr *json.SyntaxError
		if errors.As(err, &syntaxErr) {
			return validationFailedWithDetails(map[string]any{"reason": "malformed JSON", "offset": syntaxErr.Offset})
		}
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			return validationFailedWithDetails(map[string]any{"reason": "type mismatch", "field": typeErr.Field})
		}
		return errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "internal server error", err,
			errcode.WithCategory(errcode.CategoryInfra))
	}
}

func validationFailedWithDetails(details map[string]any) error {
	return errcode.New(
		errcode.KindInvalid,
		errcode.ErrValidationFailed,
		msgInvalidRequestBody,
		errcode.WithDetails(details),
		errcode.WithCategory(errcode.CategoryValidation),
	)
}

func bodyTooLargeError() error {
	return errcode.New(errcode.KindPayloadTooLarge, errcode.ErrBodyTooLarge, "request body too large",
		errcode.WithCategory(errcode.CategoryValidation))
}

func isMaxBytesError(err error) bool {
	var maxBytesErr *http.MaxBytesError
	return errors.As(err, &maxBytesErr)
}

func rejectUnknownJSONFields(body []byte, dst any) error {
	target, ok := strictJSONTargetType(dst)
	if !ok {
		return nil
	}
	return rejectUnknownJSONFieldsForType(json.RawMessage(body), target, "")
}

func strictJSONTargetType(dst any) (reflect.Type, bool) {
	t := reflect.TypeOf(dst)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == nil || t.Kind() != reflect.Struct {
		return nil, false
	}
	return t, true
}

func rejectUnknownJSONFieldsForType(raw json.RawMessage, t reflect.Type, path string) error {
	t = indirectJSONType(t)
	if t.Kind() != reflect.Struct || !isJSONObject(raw) {
		return nil
	}

	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return classifyDecodeError(err)
	}

	fields := jsonFieldTypes(t)
	for name, value := range obj {
		fieldType, ok := lookupJSONField(fields, name)
		if !ok {
			return unknownFieldError(joinJSONPath(path, name))
		}
		if err := rejectUnknownNestedJSONFields(value, fieldType, joinJSONPath(path, name)); err != nil {
			return err
		}
	}
	return nil
}

func rejectUnknownNestedJSONFields(raw json.RawMessage, t reflect.Type, path string) error {
	t = indirectJSONType(t)
	switch t.Kind() {
	case reflect.Struct:
		return rejectUnknownJSONFieldsForType(raw, t, path)
	case reflect.Slice, reflect.Array:
		elem := indirectJSONType(t.Elem())
		if elem.Kind() != reflect.Struct || !isJSONArray(raw) {
			return nil
		}
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return classifyDecodeError(err)
		}
		for _, item := range items {
			if err := rejectUnknownJSONFieldsForType(item, elem, path); err != nil {
				return err
			}
		}
	}
	return nil
}

func jsonFieldTypes(t reflect.Type) map[string]reflect.Type {
	fields := make(map[string]reflect.Type)
	for i := 0; i < t.NumField(); i++ {
		addJSONField(fields, t.Field(i))
	}
	return fields
}

func addJSONField(fields map[string]reflect.Type, f reflect.StructField) {
	if f.PkgPath != "" {
		return
	}
	name, tagged := jsonFieldName(f)
	if name == "-" {
		return
	}
	if shouldFlattenJSONField(f, tagged) {
		mergeJSONFields(fields, jsonFieldTypes(indirectJSONType(f.Type)))
		return
	}
	if name == "" {
		name = f.Name
	}
	fields[name] = f.Type
}

func shouldFlattenJSONField(f reflect.StructField, tagged bool) bool {
	if !f.Anonymous || tagged {
		return false
	}
	return indirectJSONType(f.Type).Kind() == reflect.Struct
}

func mergeJSONFields(dst, src map[string]reflect.Type) {
	for k, v := range src {
		dst[k] = v
	}
}

func jsonFieldName(f reflect.StructField) (name string, tagged bool) {
	tag, ok := f.Tag.Lookup("json")
	if !ok {
		return "", false
	}
	name, _, _ = strings.Cut(tag, ",")
	return name, true
}

func lookupJSONField(fields map[string]reflect.Type, name string) (reflect.Type, bool) {
	if t, ok := fields[name]; ok {
		return t, true
	}
	for fieldName, t := range fields {
		if strings.EqualFold(fieldName, name) {
			return t, true
		}
	}
	return nil, false
}

func indirectJSONType(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

func isJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '{'
}

func isJSONArray(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '['
}

func joinJSONPath(parent, field string) string {
	if parent == "" {
		return field
	}
	return parent + "." + field
}

func unknownFieldError(field string) error {
	return validationFailedWithDetails(map[string]any{"reason": "unknown field", "field": field})
}
