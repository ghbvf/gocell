package contracttest

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// testContractsRoot returns the testdata/contracts directory shipped with this package.
func testContractsRoot(t testing.TB) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("contracttest: runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(thisFile), "testdata", "contracts")
}

// --- pathParams (string type) ---

// TestValidatePathParam_Valid asserts that a valid value passes the path param schema.
func TestValidatePathParam_Valid(t *testing.T) {
	t.Parallel()
	root := testContractsRoot(t)
	c := LoadByID(t, root, "http.test.pathparams.v1")
	c.ValidatePathParam(t, "key", "hello")
}

// TestMustRejectPathParam_TooShort asserts that an empty string is rejected
// (violates minLength: 1).
func TestMustRejectPathParam_TooShort(t *testing.T) {
	t.Parallel()
	root := testContractsRoot(t)
	c := LoadByID(t, root, "http.test.pathparams.v1")
	c.MustRejectPathParam(t, "key", "")
}

// TestMustRejectPathParam_TooLong asserts that a value exceeding maxLength: 10
// is rejected.
//
// Critical regression guard (PR #543 review F1): paramValueToJSON used to sniff
// "12345678901" as a base-10 integer and emit a JSON number, which a string-typed
// schema then rejected via type-mismatch. That gave a false-green test — even
// removing maxLength, the assertion would still pass. After F1, "12345678901"
// against a string-typed schema is always a JSON string; rejection here proves
// the maxLength bound is what's being checked, not the conversion bug.
func TestMustRejectPathParam_TooLong(t *testing.T) {
	t.Parallel()
	root := testContractsRoot(t)
	c := LoadByID(t, root, "http.test.pathparams.v1")
	c.MustRejectPathParam(t, "key", "12345678901") // 11 chars > 10
}

// TestValidatePathParam_NumericLooking_String covers the F1 regression at the
// validate path: a numeric-looking string ("12345") must validate against a
// string-typed schema as a JSON string (it is well within minLength=1 /
// maxLength=10). If paramValueToJSON ever reverts to content-sniffing and emits
// the value as a JSON number, schema.Validate fails with a type mismatch and
// this test errors.
func TestValidatePathParam_NumericLooking_String(t *testing.T) {
	t.Parallel()
	root := testContractsRoot(t)
	c := LoadByID(t, root, "http.test.pathparams.v1")
	c.ValidatePathParam(t, "key", "12345")
}

// TestValidatePathParam_UnknownName asserts that an unknown param name causes
// t.Errorf.
func TestValidatePathParam_UnknownName(t *testing.T) {
	t.Parallel()
	root := testContractsRoot(t)
	c := LoadByID(t, root, "http.test.pathparams.v1")

	inner := &captureT{T: t}
	c.ValidatePathParam(inner, "nonexistent", "hello")
	if !inner.failed {
		t.Errorf("ValidatePathParam with unknown name should have called t.Errorf")
	}
}

// --- queryParams (integer type) ---

// TestValidateQueryParam_Valid asserts that integer "1" passes the query param schema.
func TestValidateQueryParam_Valid(t *testing.T) {
	t.Parallel()
	root := testContractsRoot(t)
	c := LoadByID(t, root, "http.test.queryparams.v1")
	c.ValidateQueryParam(t, "limit", "1")
}

// TestMustRejectQueryParam_BelowMinimum asserts that "0" violates minimum: 1.
func TestMustRejectQueryParam_BelowMinimum(t *testing.T) {
	t.Parallel()
	root := testContractsRoot(t)
	c := LoadByID(t, root, "http.test.queryparams.v1")
	c.MustRejectQueryParam(t, "limit", "0")
}

// TestMustRejectQueryParam_AboveMaximum asserts that "501" violates maximum: 500.
func TestMustRejectQueryParam_AboveMaximum(t *testing.T) {
	t.Parallel()
	root := testContractsRoot(t)
	c := LoadByID(t, root, "http.test.queryparams.v1")
	c.MustRejectQueryParam(t, "limit", "501")
}

// TestMustRejectQueryParam_WrongType asserts that a non-integer value is rejected.
// Under the schema-driven paramValueToJSON, "notanumber" fails strconv.ParseInt
// and falls back to a JSON string, which the integer schema rejects on type.
func TestMustRejectQueryParam_WrongType(t *testing.T) {
	t.Parallel()
	root := testContractsRoot(t)
	c := LoadByID(t, root, "http.test.queryparams.v1")
	c.MustRejectQueryParam(t, "limit", "notanumber")
}

// TestMustRejectQueryParam_FloatAgainstInteger asserts that "1.5" is rejected
// by an integer schema. ParseInt fails (no decimal), so the value falls back to
// a JSON string and the integer schema rejects on type.
func TestMustRejectQueryParam_FloatAgainstInteger(t *testing.T) {
	t.Parallel()
	root := testContractsRoot(t)
	c := LoadByID(t, root, "http.test.queryparams.v1")
	c.MustRejectQueryParam(t, "limit", "1.5")
}

// TestMustRejectQueryParam_UnknownName asserts that an unknown query param name
// produces a test failure.
func TestMustRejectQueryParam_UnknownName(t *testing.T) {
	t.Parallel()
	root := testContractsRoot(t)
	c := LoadByID(t, root, "http.test.queryparams.v1")

	inner := &captureT{T: t}
	c.MustRejectQueryParam(inner, "nonexistent", "1")
	if !inner.failed {
		t.Errorf("MustRejectQueryParam with unknown name should have called t.Errorf")
	}
}

// --- queryParams (boolean type) ---

// TestValidateQueryParam_BooleanTrue asserts that "true" passes a boolean schema.
func TestValidateQueryParam_BooleanTrue(t *testing.T) {
	t.Parallel()
	root := testContractsRoot(t)
	c := LoadByID(t, root, "http.test.boolparams.v1")
	c.ValidateQueryParam(t, "enabled", "true")
}

// TestValidateQueryParam_BooleanFalse asserts that "false" passes a boolean schema.
func TestValidateQueryParam_BooleanFalse(t *testing.T) {
	t.Parallel()
	root := testContractsRoot(t)
	c := LoadByID(t, root, "http.test.boolparams.v1")
	c.ValidateQueryParam(t, "enabled", "false")
}

// TestMustRejectQueryParam_BooleanWrongValue asserts that "notbool" is rejected
// by a boolean schema. ParseBool fails so the value falls back to a JSON string,
// which the boolean schema rejects on type.
func TestMustRejectQueryParam_BooleanWrongValue(t *testing.T) {
	t.Parallel()
	root := testContractsRoot(t)
	c := LoadByID(t, root, "http.test.boolparams.v1")
	c.MustRejectQueryParam(t, "enabled", "notbool")
}

// --- compileInlineParamSchema guards ---

// TestCompileInlineParamSchema_RejectsUnsupportedType asserts that
// compileInlineParamSchema calls t.Fatal when given a type not in the
// metadata.ParamTypes whitelist (e.g. "object").
func TestCompileInlineParamSchema_RejectsUnsupportedType(t *testing.T) {
	t.Parallel()

	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Errorf("compileInlineParamSchema with unsupported type should have called t.Fatalf (panic sentinel)")
			}
		}()
		// captureT.Fatalf panics to simulate test termination.
		inner := &captureT{T: t}
		compileInlineParamSchema(inner, "test/unsupported", metadata.ParamSchema{Type: "object"})
	}()
}

// --- paramValueToJSON type-driven conversion matrix ---

// TestParamValueToJSON_TypeDriven asserts that paramValueToJSON dispatches on
// ParamSchema.Type, not on string content sniffing. Each case asserts the exact
// JSON token bytes that come out so a regression to the old content-sniffing
// implementation fails immediately.
func TestParamValueToJSON_TypeDriven(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		schema   metadata.ParamSchema
		value    string
		wantJSON string
	}{
		// string → always JSON string, even when content looks numeric or bool.
		{"string/text", metadata.ParamSchema{Type: "string"}, "hello", `"hello"`},
		{"string/numeric-content", metadata.ParamSchema{Type: "string"}, "12345", `"12345"`},
		{"string/float-content", metadata.ParamSchema{Type: "string"}, "3.14", `"3.14"`},
		{"string/bool-content", metadata.ParamSchema{Type: "string"}, "true", `"true"`},
		{"string/empty", metadata.ParamSchema{Type: "string"}, "", `""`},

		// integer → JSON number on ParseInt success, JSON string fallback otherwise.
		{"integer/positive", metadata.ParamSchema{Type: "integer"}, "42", `42`},
		{"integer/negative", metadata.ParamSchema{Type: "integer"}, "-1", `-1`},
		{"integer/zero", metadata.ParamSchema{Type: "integer"}, "0", `0`},
		{"integer/non-integer", metadata.ParamSchema{Type: "integer"}, "1.5", `"1.5"`},
		{"integer/text", metadata.ParamSchema{Type: "integer"}, "notanumber", `"notanumber"`},

		// number → JSON number on finite ParseFloat success, JSON string fallback otherwise.
		{"number/float", metadata.ParamSchema{Type: "number"}, "3.14", `3.14`},
		{"number/integer-looking", metadata.ParamSchema{Type: "number"}, "42", `42`},
		{"number/NaN", metadata.ParamSchema{Type: "number"}, "NaN", `"NaN"`},
		{"number/Inf", metadata.ParamSchema{Type: "number"}, "Inf", `"Inf"`},
		{"number/+Inf", metadata.ParamSchema{Type: "number"}, "+Inf", `"+Inf"`},
		{"number/-Inf", metadata.ParamSchema{Type: "number"}, "-Inf", `"-Inf"`},
		{"number/text", metadata.ParamSchema{Type: "number"}, "notanumber", `"notanumber"`},

		// boolean → "true"/"false" on ParseBool success, JSON string fallback otherwise.
		{"boolean/true", metadata.ParamSchema{Type: "boolean"}, "true", `true`},
		{"boolean/false", metadata.ParamSchema{Type: "boolean"}, "false", `false`},
		{"boolean/1", metadata.ParamSchema{Type: "boolean"}, "1", `true`},
		{"boolean/0", metadata.ParamSchema{Type: "boolean"}, "0", `false`},
		{"boolean/text", metadata.ParamSchema{Type: "boolean"}, "yes", `"yes"`},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := string(paramValueToJSON(tc.schema, tc.value))
			if got != tc.wantJSON {
				t.Errorf("paramValueToJSON(%+v, %q) = %s, want %s", tc.schema, tc.value, got, tc.wantJSON)
			}
		})
	}
}

// captureT wraps *testing.T and records whether Errorf or Fatalf was called,
// without propagating the failure to the outer test. Used to assert that the
// API produces a test error for invalid inputs.
type captureT struct {
	*testing.T
	failed bool
}

// Helper is overridden to prevent the call stack frame from pointing into the
// real *testing.T internals, which would misattribute error line numbers.
func (c *captureT) Helper() {}

func (c *captureT) Errorf(format string, args ...any) {
	c.failed = true
	// Do not forward to c.T to avoid failing the outer test.
}

// Fatalf records the failure and panics to simulate test termination
// (mirroring mockTB.Fatalf semantics used elsewhere in this package).
func (c *captureT) Fatalf(format string, args ...any) {
	c.failed = true
	panic("captureT.Fatalf called")
}
