package schemavalidate

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// schemaForTest compiles a schema from a JSON string and fails the test on error.
func schemaForTest(t *testing.T, schemaJSON string) Validator {
	t.Helper()
	v, err := NewValidator([]byte(schemaJSON))
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	return v
}

func TestValidator_HappyPath(t *testing.T) {
	v := schemaForTest(t, `{
		"type": "object",
		"properties": {
			"name": {"type": "string", "minLength": 3}
		},
		"required": ["name"],
		"additionalProperties": false
	}`)

	if err := v.Validate(context.Background(), []byte(`{"name":"alice"}`)); err != nil {
		t.Errorf("expected no error for valid input, got: %v", err)
	}
}

func TestValidator_MinLength(t *testing.T) {
	v := schemaForTest(t, `{
		"type": "object",
		"properties": {
			"name": {"type": "string", "minLength": 3}
		},
		"additionalProperties": false
	}`)

	err := v.Validate(context.Background(), []byte(`{"name":"ab"}`))
	if err == nil {
		t.Fatal("expected error for minLength violation, got nil")
	}

	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
	if ec.Code != errcode.ErrValidationFailed {
		t.Errorf("expected code ErrValidationFailed, got %q", ec.Code)
	}
	// Must not expose the specific length value in the message.
	msg := ec.Message
	if containsLengthOracle(msg) {
		t.Errorf("message exposes schema internals (oracle): %q", msg)
	}
	// Must contain field name.
	if !containsFieldName(msg, "name") {
		t.Errorf("message should contain field name 'name', got: %q", msg)
	}
}

func TestValidator_MaxLength(t *testing.T) {
	v := schemaForTest(t, `{
		"type": "object",
		"properties": {
			"tag": {"type": "string", "maxLength": 5}
		},
		"additionalProperties": false
	}`)

	err := v.Validate(context.Background(), []byte(`{"tag":"toolongvalue"}`))
	if err == nil {
		t.Fatal("expected error for maxLength violation")
	}

	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("expected *errcode.Error, got %T", err)
	}
	if ec.Code != errcode.ErrValidationFailed {
		t.Errorf("code = %q, want ErrValidationFailed", ec.Code)
	}
	if containsLengthOracle(ec.Message) {
		t.Errorf("message exposes oracle: %q", ec.Message)
	}
}

func TestValidator_MinimumOnIntField(t *testing.T) {
	v := schemaForTest(t, `{
		"type": "object",
		"properties": {
			"count": {"type": "integer", "minimum": 1}
		},
		"additionalProperties": false
	}`)

	err := v.Validate(context.Background(), []byte(`{"count": 0}`))
	if err == nil {
		t.Fatal("expected error for minimum violation")
	}

	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("expected *errcode.Error, got %T", err)
	}
	if ec.Code != errcode.ErrValidationFailed {
		t.Errorf("code = %q, want ErrValidationFailed", ec.Code)
	}
}

func TestValidator_PatternRegex(t *testing.T) {
	v := schemaForTest(t, `{
		"type": "object",
		"properties": {
			"slug": {"type": "string", "pattern": "^[a-z]+$"}
		},
		"additionalProperties": false
	}`)

	err := v.Validate(context.Background(), []byte(`{"slug":"UPPER"}`))
	if err == nil {
		t.Fatal("expected error for pattern violation")
	}

	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("expected *errcode.Error, got %T", err)
	}
	if ec.Code != errcode.ErrValidationFailed {
		t.Errorf("code = %q, want ErrValidationFailed", ec.Code)
	}
	// Must not expose the regex pattern.
	if containsPatternOracle(ec.Message) {
		t.Errorf("message exposes pattern oracle: %q", ec.Message)
	}
}

func TestValidator_RequiredMissing(t *testing.T) {
	v := schemaForTest(t, `{
		"type": "object",
		"properties": {
			"username": {"type": "string"},
			"password": {"type": "string"}
		},
		"required": ["username", "password"],
		"additionalProperties": false
	}`)

	err := v.Validate(context.Background(), []byte(`{"username":"alice"}`))
	if err == nil {
		t.Fatal("expected error for missing required field")
	}

	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("expected *errcode.Error, got %T", err)
	}
	if ec.Code != errcode.ErrValidationFailed {
		t.Errorf("code = %q, want ErrValidationFailed", ec.Code)
	}
}

func TestValidator_AdditionalPropertiesFalse(t *testing.T) {
	v := schemaForTest(t, `{
		"type": "object",
		"properties": {
			"name": {"type": "string"}
		},
		"required": ["name"],
		"additionalProperties": false
	}`)

	err := v.Validate(context.Background(), []byte(`{"name":"alice","extra":"field"}`))
	if err == nil {
		t.Fatal("expected error for additional properties")
	}

	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("expected *errcode.Error, got %T", err)
	}
	if ec.Code != errcode.ErrValidationFailed {
		t.Errorf("code = %q, want ErrValidationFailed", ec.Code)
	}
}

func TestValidator_MultipleViolations(t *testing.T) {
	// Both name (minLength) and count (minimum) violated.
	v := schemaForTest(t, `{
		"type": "object",
		"properties": {
			"name": {"type": "string", "minLength": 3},
			"count": {"type": "integer", "minimum": 1}
		},
		"required": ["name", "count"],
		"additionalProperties": false
	}`)

	err := v.Validate(context.Background(), []byte(`{"name":"ab","count":0}`))
	if err == nil {
		t.Fatal("expected error for multiple violations")
	}

	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("expected *errcode.Error, got %T", err)
	}
	if ec.Code != errcode.ErrValidationFailed {
		t.Errorf("code = %q, want ErrValidationFailed", ec.Code)
	}
}

func TestValidator_RuneLengthBoundary(t *testing.T) {
	// JSON Schema minLength counts Unicode code points (runes), not bytes.
	// Chinese character "你" is 3 bytes UTF-8 but 1 rune.
	// minLength: 3 means at least 3 runes, so "你好" (2 runes) should fail.
	v := schemaForTest(t, `{
		"type": "object",
		"properties": {
			"label": {"type": "string", "minLength": 3}
		},
		"additionalProperties": false
	}`)

	// "你好" = 2 Chinese chars = 2 runes, 6 bytes — should FAIL minLength=3.
	err := v.Validate(context.Background(), []byte(`{"label":"你好"}`))
	if err == nil {
		t.Error("expected error for 2-rune string against minLength=3 (rune-based check)")
	}

	// "你好世" = 3 Chinese chars = 3 runes, 9 bytes — should PASS minLength=3.
	if err2 := v.Validate(context.Background(), []byte(`{"label":"你好世"}`)); err2 != nil {
		t.Errorf("expected no error for 3-rune string against minLength=3, got: %v", err2)
	}
}

func TestValidator_ErrorTypeIsErrcode(t *testing.T) {
	v := schemaForTest(t, `{
		"type": "object",
		"properties": {
			"x": {"type": "string", "minLength": 5}
		},
		"additionalProperties": false
	}`)

	err := v.Validate(context.Background(), []byte(`{"x":"ab"}`))
	if err == nil {
		t.Fatal("expected error")
	}

	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("error is not *errcode.Error: %T: %v", err, err)
	}
	if ec.Code != errcode.ErrValidationFailed {
		t.Errorf("Code = %q, want ErrValidationFailed", ec.Code)
	}
}

func TestWriteValidationError_WritesHTTP400(t *testing.T) {
	err := errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "name: invalid")
	w := httptest.NewRecorder()
	WriteValidationError(context.Background(), w, err)

	if w.Code != http.StatusBadRequest {
		t.Errorf("HTTP status = %d, want 400", w.Code)
	}
}

func TestWriteValidationError_WrapsPlainError(t *testing.T) {
	plainErr := errors.New("something went wrong")
	w := httptest.NewRecorder()
	WriteValidationError(context.Background(), w, plainErr)

	if w.Code != http.StatusBadRequest {
		t.Errorf("HTTP status = %d, want 400", w.Code)
	}
}

// containsLengthOracle reports whether s contains oracle-leaking substrings
// that would expose constraint values to clients.
func containsLengthOracle(s string) bool {
	oracleKeywords := []string{
		"too short", "too long", "minimum", "maximum",
		"minLength", "maxLength", "must be at least", "must be at most",
		"must be longer", "must be shorter", "characters",
	}
	lower := s
	for _, kw := range oracleKeywords {
		if contains(lower, kw) {
			return true
		}
	}
	return false
}

// containsPatternOracle reports whether s exposes regex pattern details.
func containsPatternOracle(s string) bool {
	patternKeywords := []string{"pattern", "regex", "regexp", "^", "$"}
	for _, kw := range patternKeywords {
		if contains(s, kw) {
			return true
		}
	}
	return false
}

// containsFieldName reports whether s contains fieldName.
func containsFieldName(s, fieldName string) bool {
	return contains(s, fieldName)
}

// contains is a case-insensitive substring check.
func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		indexCI(s, sub) >= 0)
}

func indexCI(s, sub string) int {
	ls, lsub := len(s), len(sub)
	if lsub == 0 {
		return 0
	}
	for i := 0; i <= ls-lsub; i++ {
		if equalCI(s[i:i+lsub], sub) {
			return i
		}
	}
	return -1
}

func equalCI(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		ac, bc := a[i], b[i]
		if ac >= 'A' && ac <= 'Z' {
			ac += 32
		}
		if bc >= 'A' && bc <= 'Z' {
			bc += 32
		}
		if ac != bc {
			return false
		}
	}
	return true
}
