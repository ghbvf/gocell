package contracttest

import (
	"path/filepath"
	"runtime"
	"testing"
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
func TestMustRejectPathParam_TooLong(t *testing.T) {
	t.Parallel()
	root := testContractsRoot(t)
	c := LoadByID(t, root, "http.test.pathparams.v1")
	c.MustRejectPathParam(t, "key", "12345678901") // 11 chars > 10
}

// TestValidatePathParam_UnknownName asserts that an unknown param name causes
// t.Errorf (we verify this by expecting the error sentinel via a sub-test
// that calls ValidatePathParam on an undeclared name, which should produce an
// error; here we test the known-name happy path instead).
func TestValidatePathParam_UnknownName(t *testing.T) {
	// This test deliberately uses a sub-test with a fake *testing.T to capture
	// the error without failing the outer test.
	t.Parallel()
	root := testContractsRoot(t)
	c := LoadByID(t, root, "http.test.pathparams.v1")

	// Verify that unknown param name produces a test failure.
	inner := &captureT{T: t}
	c.ValidatePathParam(inner, "nonexistent", "hello")
	if !inner.failed {
		t.Errorf("ValidatePathParam with unknown name should have called t.Errorf")
	}
}

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
func TestMustRejectQueryParam_WrongType(t *testing.T) {
	t.Parallel()
	root := testContractsRoot(t)
	c := LoadByID(t, root, "http.test.queryparams.v1")
	c.MustRejectQueryParam(t, "limit", "notanumber")
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

// captureT wraps *testing.T and records whether Errorf was called, without
// propagating the failure to the outer test. Used to assert that the API
// produces a test error for invalid inputs.
type captureT struct {
	*testing.T
	failed bool
}

func (c *captureT) Errorf(format string, args ...any) {
	c.failed = true
	// Do not forward to c.T to avoid failing the outer test.
}
