// Package schemavalidate provides JSON Schema validation for generated HTTP handlers.
//
// It loads a JSON Schema (draft 2020-12) from raw bytes, compiles it once at
// handler construction time, and validates request bodies on every request.
// Validation errors are mapped to errcode.ErrValidationFailed and written
// via httputil.WriteError. Error messages expose field names but never expose
// schema-internal details (lengths, ranges, patterns) to prevent oracle attacks.
//
// ref: santhosh-tekuri/jsonschema/v6 (already in go.mod via contracttest)
// ref: deepmap/oapi-codegen security examples (request validation patterns)
package schemavalidate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
)

// Validator validates a JSON payload against a compiled JSON Schema.
type Validator interface {
	// Validate validates body against the compiled schema.
	// Returns nil on success. Returns *errcode.Error (code=ErrValidationFailed)
	// on schema violation. Error messages contain field names but never
	// expose schema internals (lengths, ranges, regex patterns).
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

// WriteValidationError writes an HTTP 400 response with the error from Validate.
// If err is not an *errcode.Error it is wrapped as ErrValidationFailed.
func WriteValidationError(ctx context.Context, w http.ResponseWriter, err error) {
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		ec = errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, err.Error())
	}
	httputil.WriteError(ctx, w, ec)
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
			return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, msg)
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
