// Package schemavalidate provides transport-neutral JSON Schema validation for
// generated code at untrusted wire boundaries.
//
// It loads a JSON Schema (draft 2020-12) from raw bytes, compiles it once at
// construction time, and validates payloads on every call. Validation errors are
// mapped to errcode.ErrValidationFailed. Error messages expose field names but
// never expose schema-internal details (lengths, ranges, patterns) to prevent
// oracle attacks.
//
// Both transports embed and reuse this validator at their untrusted ingress:
//   - HTTP handlers (generated/contracts/http/**): validate the request body
//     bytes before entering the cell (handler.tmpl).
//   - Async command dispatch (generated/contracts/command/**): validate the
//     outbox entry payload bytes inside DispatchAsync before unmarshal+handle
//     (command.tmpl, #1588). The sync in-process Dispatch deliberately does NOT
//     validate — it is a first-party typed boundary (ADR 202606040550-1044 §D8).
//
// HTTP response writing for a validation error is the caller's concern:
// generated HTTP handlers pass the returned *errcode.Error straight to
// httputil.WriteError (KindInvalid → 400); this package stays response-writer
// free so non-HTTP consumers (command dispatch) can reuse it without pulling in
// net/http.
//
// ref: santhosh-tekuri/jsonschema/v6 (already in go.mod via contracttest)
// ref: deepmap/oapi-codegen security examples (request validation patterns)
package schemavalidate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// Validator validates a JSON payload against a compiled JSON Schema.
type Validator interface {
	// Validate validates body against the compiled schema.
	// Returns nil on success. Returns *errcode.Error (code=ErrValidationFailed)
	// on schema violation.
	//
	// Error shape: the offending field path is carried in the "detail"
	// PublicDetail (oracle-safe — field name only, never the constraint value:
	// lengths, ranges, regex patterns). The Message is a fixed const literal
	// ("request body validation failed"). Because (*errcode.Error).Error() renders
	// only [Code] + Message (+ Cause), NOT Details, a caller that logs err.Error()
	// — e.g. the outbox relay storing last_error via SanitizeError — never leaks
	// the field name. The field path reaches clients only via the wire-serialized
	// details array (4xx), not server-side error strings.
	//
	// ctx is accepted for API stability and future cancellation/deadline support;
	// the default implementation does not use it (validation is CPU-bound, in-memory).
	Validate(ctx context.Context, body []byte) error
}

// NewValidator compiles schemaJSON as a JSON Schema (draft 2020-12) and returns
// a Validator. The compilation cost is paid once at construction time; each
// call to Validate is schema-free.
//
// Returns error if schemaJSON is not valid JSON or is not a compilable schema.
func NewValidator(schemaJSON []byte) (Validator, error) {
	var doc any
	if err := json.Unmarshal(schemaJSON, &doc); err != nil {
		return nil, fmt.Errorf("schemavalidate: invalid JSON: %w", err)
	}

	compiler := jsonschema.NewCompiler()
	const schemaURL = "mem:///request.schema.json"
	if err := compiler.AddResource(schemaURL, doc); err != nil {
		return nil, fmt.Errorf("schemavalidate: add schema resource: %w", err)
	}

	schema, err := compiler.Compile(schemaURL)
	if err != nil {
		return nil, fmt.Errorf("schemavalidate: compile schema: %w", err)
	}

	return &validator{schema: schema}, nil
}

// validator is the concrete implementation backed by santhosh-tekuri/jsonschema/v6.
type validator struct {
	schema *jsonschema.Schema
}

// Validate implements Validator.
func (v *validator) Validate(_ context.Context, body []byte) error {
	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "invalid JSON body")
	}

	if err := v.schema.Validate(doc); err != nil {
		var verr *jsonschema.ValidationError
		if errors.As(err, &verr) {
			msg := buildSafeMessage(verr)
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "request body validation failed",
				errcode.WithDetails(errcode.PublicString("detail", msg)))
		}
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "invalid request body")
	}
	return nil
}

// buildSafeMessage constructs a client-visible error message from a ValidationError
// tree. It collects the first leaf error's field path and returns "field: invalid".
// Multiple violations collapse to the first meaningful field path to avoid
// enumerating the full constraint list (oracle prevention).
func buildSafeMessage(e *jsonschema.ValidationError) string {
	leaves := collectLeafErrors(e)
	if len(leaves) == 0 {
		return "invalid"
	}
	// Return first leaf message; multiple violations still produce a safe message.
	return leaves[0]
}

// sanitizeMessage converts a jsonschema validation error to a safe
// client-visible string. It extracts the field name (instance path) but never
// includes the specific constraint value (length, range, pattern).
func sanitizeMessage(e *jsonschema.ValidationError) string {
	if e == nil {
		return "invalid"
	}
	field := instanceField(e.InstanceLocation)
	if field == "" {
		return "invalid"
	}
	return field + ": invalid"
}

// instanceField extracts a dot-joined field path from a JSON Pointer instance
// location ([]string). []string{"username"} → "username",
// []string{"nested", "field"} → "nested.field", nil/empty → "".
func instanceField(loc []string) string {
	if len(loc) == 0 {
		return ""
	}
	return strings.Join(loc, ".")
}

// collectLeafErrors walks the ValidationError tree and collects leaf messages.
// Leaves are errors that have no Causes (i.e., no nested failures).
func collectLeafErrors(e *jsonschema.ValidationError) []string {
	if len(e.Causes) == 0 {
		return []string{sanitizeMessage(e)}
	}
	var msgs []string
	for _, cause := range e.Causes {
		msgs = append(msgs, collectLeafErrors(cause)...)
	}
	return msgs
}
