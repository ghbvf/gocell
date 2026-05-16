// Package errcodetest provides typed assertion funnels for errcode-bearing
// test paths. The two exported funnels below are the only sanctioned shape
// for tests whose name ends in _NotFound (and table cases ending in
// _NotFound), enforced by archtest
// POSTGRES-NOTFOUND-TEST-OTHER-ERROR-MIXUP-ARCHTEST-01.
//
// Picking either funnel — but no other shape — is what makes that rule
// "violation impossible to express" at the assertion site
// (.claude/rules/gocell/ai-collab.md §"Hard 范本" / typed function call as
// Hard funnel for unbounded operations; same template as
// pkg/panicregister.Approved + PANIC-REGISTERED-01).
//
// Upstream funnel enforcement (archtest POSTGRES-NOTFOUND-TEST-OTHER-ERROR-MIXUP-ARCHTEST-01)
// lands in the immediate follow-up PR (docs/backlog/cap-14-tooling.md:18 —
// R2-P3 PR-b 212-pg-notfound-archtest). Until that PR merges, the funnel is
// downstream-Hard (calling AssertCode/AssertWireCode is type-bound) but
// upstream-Soft (a _NotFound test can currently still write inline assertions
// without being rejected). This is an ai-collab.md §"Funnel 双向锁评级"
// transitional form, registered explicitly to prevent silent carry-over.
//
// Per pkg/ layering: the package depends only on the standard library and
// pkg/errcode (a sibling of this package). It does not import third-party
// assertion frameworks because pkg/.go files must stay on the standard
// library; see .claude/rules/gocell/go-standards.md.
package errcodetest

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// AssertCode unwraps err via errors.As to *errcode.Error and asserts that its
// Code field equals expected. It calls t.Helper() so test output blames the
// caller line.
//
// Use in service / kernel / runtime / adapters tests where err is a direct
// service / repo return value (not an HTTP wire envelope).
//
// Fail-closed conditions, each via t.Fatalf so the test halts on the funnel
// violation rather than on a downstream nil dereference:
//
//   - err == nil — the test expected a NotFound error but received nil
//   - err does not chain to *errcode.Error via errors.As — the test path
//     is returning a bare/sentinel error, which is the exact mutation-test
//     hazard this funnel exists to forbid (see ADR cap-14:18 motivation)
//
// On a Code mismatch the funnel calls t.Errorf (non-fatal) so subsequent
// assertions in the same test still run.
func AssertCode(t testing.TB, err error, expected errcode.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("errcodetest.AssertCode: expected error with Code=%s, got nil", expected)
	}
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		t.Fatalf("errcodetest.AssertCode: expected *errcode.Error chain "+
			"with Code=%s, got %T: %v", expected, err, err)
	}
	if ec.Code != expected {
		t.Errorf("errcodetest.AssertCode: Code mismatch — want %s, got %s "+
			"(message=%q)", expected, ec.Code, ec.Message)
	}
}

// wireEnvelope mirrors contracts/shared/errors/error-response-v1.schema.json.
// The outer object wraps a single "error" object holding the canonical
// errcode.Error projection (code + message + details + optional request_id).
// Only the fields this funnel needs to assert are decoded; unknown sibling
// fields (request_id, etc.) round-trip transparently.
type wireEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// AssertWireCode parses rec.Body as the shared wire error envelope and
// asserts both the HTTP status code and the envelope's error.code match
// expected.
//
// Use in HTTP handler tests served through *httptest.ResponseRecorder.
//
// Fail-closed conditions (each via t.Fatalf):
//
//   - rec == nil or rec.Body == nil — the test never served a response
//   - rec.Code != expectedStatus — handler did not reach the expected
//     wire shape; reporting both status and body excerpt aids triage
//   - body is empty or not JSON-decodable as the wire envelope shape
//
// On code mismatch the funnel calls t.Errorf (non-fatal) so subsequent
// assertions in the same test still run.
func AssertWireCode(t testing.TB, rec *httptest.ResponseRecorder, expectedStatus int, expected errcode.Code) {
	t.Helper()
	if rec == nil {
		t.Fatalf("errcodetest.AssertWireCode: rec is nil; test never served a response")
	}
	if rec.Body == nil {
		t.Fatalf("errcodetest.AssertWireCode: rec.Body is nil; " +
			"use httptest.NewRecorder() instead of &httptest.ResponseRecorder{}")
	}
	if rec.Code != expectedStatus {
		t.Fatalf("errcodetest.AssertWireCode: HTTP status mismatch — "+
			"want %d, got %d; body=%q", expectedStatus, rec.Code, rec.Body.String())
	}
	body := rec.Body.Bytes()
	if len(body) == 0 {
		t.Fatalf("errcodetest.AssertWireCode: response body is empty; "+
			"expected wire envelope with Code=%s", expected)
	}
	var env wireEnvelope
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&env); err != nil {
		t.Fatalf("errcodetest.AssertWireCode: body is not the wire "+
			"envelope shape: %v; body=%q", err, string(body))
	}
	if env.Error.Code != string(expected) {
		t.Errorf("errcodetest.AssertWireCode: error.code mismatch — "+
			"want %s, got %s (message=%q)", expected, env.Error.Code, env.Error.Message)
	}
}
