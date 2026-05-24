package ledger_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
)

// Compile-time verification that package-internal types implement the sealed
// marker interfaces. The marker methods (restartRecoveryModeOK /
// idempotencyModeOK) are unexported, so package-external types cannot satisfy
// these interfaces — any attempt to add an external implementer would fail to
// compile here.
var (
	_ ledger.RestartRecoveryMode = ledger.RestartRecoveryStrictTailVerify{}
	_ ledger.IdempotencyMode     = ledger.IdempotencyContentFingerprint{}
)

// externalRestartRecovery is a local type that CANNOT implement
// RestartRecoveryMode (the marker method is unexported). This remains as a
// compile-time documentation that external types cannot satisfy the interface.
// type externalRestartRecovery struct{}
// func (externalRestartRecovery) restartRecoveryModeOK() {} // would not compile

// containsAny returns true if s contains any of the given substrings.
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// TestNewProtocol_NoOptions_Error: NewProtocol with zero options must fail
// because all 4 wiring options are required.
func TestNewProtocol_NoOptions_Error(t *testing.T) {
	t.Parallel()
	p, err := ledger.NewProtocol()
	if err == nil {
		t.Fatalf("expected error for missing required options, got nil; protocol=%+v", p)
	}
	if p != nil {
		t.Fatalf("expected nil protocol on error, got %+v", p)
	}
	var coded *errcode.Error
	if !errors.As(err, &coded) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
	if coded.Code != errcode.ErrValidationFailed {
		t.Errorf("expected ErrValidationFailed, got %s", coded.Code)
	}
}

// TestNewProtocol_AllOptions_OK: providing all 4 required options succeeds.
func TestNewProtocol_AllOptions_OK(t *testing.T) {
	t.Parallel()
	hmacKey := make([]byte, 32)
	for i := range hmacKey {
		hmacKey[i] = byte(i + 1)
	}
	ns, err := ledger.ParseNamespaceID("auditcore")
	if err != nil {
		t.Fatalf("ParseNamespaceID: %v", err)
	}
	p, err := ledger.NewProtocol(
		ledger.WithChainHMAC(hmacKey),
		ledger.WithNamespace(ns),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil protocol")
	}
}

// assertHMACKeyError checks that NewProtocol with a given key length behaves as
// expected and, on error, does not leak key material. Extracted to reduce the
// cognitive complexity of TestNewProtocol_HMACKeyTooShort (go:S3776 CC=22).
func assertHMACKeyError(t *testing.T, ns ledger.NamespaceID, keyLen int, wantErr bool) {
	t.Helper()
	key := make([]byte, keyLen)
	_, err := ledger.NewProtocol(
		ledger.WithChainHMAC(key),
		ledger.WithNamespace(ns),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	if wantErr && err == nil {
		t.Fatalf("expected error for key length %d, got nil", keyLen)
	}
	if !wantErr && err != nil {
		t.Fatalf("unexpected error for key length %d: %v", keyLen, err)
	}
	if !wantErr || err == nil {
		return
	}
	// Must not expose key material in error message.
	var coded *errcode.Error
	if !errors.As(err, &coded) {
		t.Fatalf("expected *errcode.Error, got %T: %v", err, err)
	}
	// Only check for key material leakage when the key is non-empty.
	if len(key) > 0 && strings.Contains(coded.Message, string(key)) {
		t.Error("error message must not contain key material")
	}
}

// TestNewProtocol_HMACKeyTooShort: keys shorter than 32 bytes must be rejected.
func TestNewProtocol_HMACKeyTooShort(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		keyLen  int
		wantErr bool
	}{
		{"empty_key", 0, true},
		{"31_bytes", 31, true},
		{"32_bytes", 32, false},
		{"64_bytes", 64, false},
	}
	ns, _ := ledger.ParseNamespaceID("auditcore")
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertHMACKeyError(t, ns, tc.keyLen, tc.wantErr)
		})
	}
}

// TestNamespaceID_Validate: NamespaceID.Validate rejects bad values.
func TestNamespaceID_Validate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"empty", "", true},
		{"contains_colon", "audit:core", true},
		{"contains_upper", "AuditCore", true},
		{"too_long_49", strings.Repeat("a", 49), true},
		{"max_48", strings.Repeat("a", 48), false},
		{"starts_digit", "1audit", true},
		{"starts_dash", "-audit", true},
		{"starts_underscore", "_audit", false},
		{"valid_simple", "auditcore", false},
		{"valid_with_dash", "audit-core", false},
		{"valid_with_underscore", "audit_core", false},
		{"contains_brace_open", "audit{core", true},
		{"contains_brace_close", "audit}core", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ns := ledger.NamespaceID(tc.input)
			err := ns.Validate()
			if tc.wantErr && err == nil {
				t.Errorf("Validate(%q): expected error, got nil", tc.input)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("Validate(%q): unexpected error: %v", tc.input, err)
			}
		})
	}
}

// TestNewProtocol_WithRestartRecoveryNil_Rejected: typed-nil RestartRecoveryMode rejected.
func TestNewProtocol_WithRestartRecoveryNil_Rejected(t *testing.T) {
	t.Parallel()
	hmacKey := make([]byte, 32)
	ns, _ := ledger.ParseNamespaceID("auditcore")
	var rr ledger.RestartRecoveryMode // typed nil
	_, err := ledger.NewProtocol(
		ledger.WithChainHMAC(hmacKey),
		ledger.WithNamespace(ns),
		ledger.WithRestartRecovery(rr),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	if err == nil {
		t.Fatal("expected error for nil RestartRecoveryMode")
	}
	if !strings.Contains(err.Error(), "restart") {
		t.Errorf("expected error to mention restart, got %q", err.Error())
	}
}

// TestNewProtocol_WithIdempotencyNil_Rejected: typed-nil IdempotencyMode rejected.
func TestNewProtocol_WithIdempotencyNil_Rejected(t *testing.T) {
	t.Parallel()
	hmacKey := make([]byte, 32)
	ns, _ := ledger.ParseNamespaceID("auditcore")
	var im ledger.IdempotencyMode // typed nil
	_, err := ledger.NewProtocol(
		ledger.WithChainHMAC(hmacKey),
		ledger.WithNamespace(ns),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(im),
	)
	if err == nil {
		t.Fatal("expected error for nil IdempotencyMode")
	}
	if !strings.Contains(err.Error(), "idempotency") {
		t.Errorf("expected error to mention idempotency, got %q", err.Error())
	}
}

// TestNewProtocol_WithHMACNil_Rejected: nil HMAC key rejected (no key = no chain).
func TestNewProtocol_WithHMACNil_Rejected(t *testing.T) {
	t.Parallel()
	ns, _ := ledger.ParseNamespaceID("auditcore")
	_, err := ledger.NewProtocol(
		ledger.WithChainHMAC(nil),
		ledger.WithNamespace(ns),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	if err == nil {
		t.Fatal("expected error for nil HMAC key")
	}
}

// TestNewProtocol_WithNamespaceMissing_Rejected: missing namespace is rejected.
func TestNewProtocol_WithNamespaceMissing_Rejected(t *testing.T) {
	t.Parallel()
	hmacKey := make([]byte, 32)
	_, err := ledger.NewProtocol(
		ledger.WithChainHMAC(hmacKey),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	if err == nil {
		t.Fatal("expected error for missing namespace")
	}
	if !strings.Contains(err.Error(), "namespace") {
		t.Errorf("expected error to mention namespace, got %q", err.Error())
	}
}

// ---------------------------------------------------------------------------
// With* Option nil → immediate error (short-circuit, no sentinel sticky).
//
// Each Option func returns a non-nil error on receiving an invalid input
// (nil / empty / zero-value), causing NewProtocol to stop applying subsequent
// options. The pattern below inserts an invalid option followed by a valid one
// and asserts the immediate error wins — the second valid call cannot mask the
// first invalid call.
// ---------------------------------------------------------------------------

// TestWithChainHMAC_NilReturnsError_Immediate verifies that WithChainHMAC(nil)
// returns an error immediately so NewProtocol short-circuits and does not
// execute subsequent options. The observable: passing WithChainHMAC(nil) then
// WithChainHMAC(validKey) must still fail, and the error message must mention
// "nil"/"empty" (immediate error from the Option func itself, not a deferred
// "key required" message).
func TestWithChainHMAC_NilReturnsError_Immediate(t *testing.T) {
	t.Parallel()
	ns, _ := ledger.ParseNamespaceID("auditcore")
	validKey := make([]byte, 32)
	for i := range validKey {
		validKey[i] = byte(i + 1)
	}

	_, err := ledger.NewProtocol(
		ledger.WithChainHMAC(nil),
		ledger.WithChainHMAC(validKey),
		ledger.WithNamespace(ns),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	if err == nil {
		t.Fatal("WithChainHMAC(nil) followed by valid key must still return error")
	}
	errStr := err.Error()
	// Immediate error must mention "nil" or "empty" — not a deferred "key required" sentinel.
	if !containsAny(errStr, "nil", "empty", "missing key") {
		t.Errorf("WithChainHMAC(nil) should immediately return error "+
			"mentioning 'nil' or 'empty', got %q", errStr)
	}
}

// TestWithNamespace_EmptyReturnsError_Immediate verifies that WithNamespace("")
// returns an error immediately so NewProtocol short-circuits and does not
// apply the subsequent valid namespace.
func TestWithNamespace_EmptyReturnsError_Immediate(t *testing.T) {
	t.Parallel()
	validKey := make([]byte, 32)
	for i := range validKey {
		validKey[i] = byte(i + 1)
	}
	validNS, _ := ledger.ParseNamespaceID("auditcore")

	// Passing empty namespace then valid namespace: short-circuit semantics =
	// valid NS option never runs because the first call returns an error.
	_, err := ledger.NewProtocol(
		ledger.WithChainHMAC(validKey),
		ledger.WithNamespace(""), // empty NamespaceID = typed zero value
		ledger.WithNamespace(validNS),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	if err == nil {
		t.Fatal("WithNamespace(\"\") followed by valid namespace must still return error")
	}
	errStr := err.Error()
	// Immediate error must mention "empty" — not a deferred "namespace required" sentinel.
	if !containsAny(errStr, "empty", "must not be empty", "namespace ID must not be empty") {
		t.Errorf("WithNamespace(\"\") should immediately return error "+
			"mentioning empty namespace, got %q", errStr)
	}
}

// TestWithRestartRecovery_NilReturnsError_Immediate verifies that
// WithRestartRecovery(nil) returns an error immediately so NewProtocol
// short-circuits and does not apply the subsequent valid mode.
func TestWithRestartRecovery_NilReturnsError_Immediate(t *testing.T) {
	t.Parallel()
	validKey := make([]byte, 32)
	for i := range validKey {
		validKey[i] = byte(i + 1)
	}
	ns, _ := ledger.ParseNamespaceID("auditcore")
	var nilRR ledger.RestartRecoveryMode // typed nil

	// nil then valid: short-circuit means the valid mode never runs.
	_, err := ledger.NewProtocol(
		ledger.WithChainHMAC(validKey),
		ledger.WithNamespace(ns),
		ledger.WithRestartRecovery(nilRR),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	if err == nil {
		t.Fatal("WithRestartRecovery(nil) followed by valid mode must still return error")
	}
	errStr := err.Error()
	// Immediate error must mention nil/invalid — not a deferred "mode required" sentinel.
	if !containsAny(errStr, "nil", "invalid", "must not be nil") {
		t.Errorf("WithRestartRecovery(nil) should immediately error "+
			"mentioning nil/invalid, got %q", errStr)
	}
}

// TestWithIdempotency_NilReturnsError_Immediate verifies that
// WithIdempotency(nil) returns an error immediately so NewProtocol
// short-circuits and does not apply the subsequent valid mode.
func TestWithIdempotency_NilReturnsError_Immediate(t *testing.T) {
	t.Parallel()
	validKey := make([]byte, 32)
	for i := range validKey {
		validKey[i] = byte(i + 1)
	}
	ns, _ := ledger.ParseNamespaceID("auditcore")
	var nilIM ledger.IdempotencyMode // typed nil

	// nil then valid: short-circuit means the valid mode never runs.
	_, err := ledger.NewProtocol(
		ledger.WithChainHMAC(validKey),
		ledger.WithNamespace(ns),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(nilIM),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	if err == nil {
		t.Fatal("WithIdempotency(nil) followed by valid mode must still return error")
	}
	errStr := err.Error()
	// Immediate error must mention nil/invalid — not a deferred "mode required" sentinel.
	if !containsAny(errStr, "nil", "invalid", "must not be nil") {
		t.Errorf("WithIdempotency(nil) should immediately error "+
			"mentioning nil/invalid, got %q", errStr)
	}
}

// TestNewProtocol_OK: NewProtocol succeeds with valid options.
func TestNewProtocol_OK(t *testing.T) {
	t.Parallel()
	hmacKey := make([]byte, 32)
	for i := range hmacKey {
		hmacKey[i] = byte(i + 1)
	}
	ns, _ := ledger.ParseNamespaceID("auditcore")
	p, err := ledger.NewProtocol(
		ledger.WithChainHMAC(hmacKey),
		ledger.WithNamespace(ns),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	if err != nil {
		t.Fatalf("unexpected error from NewProtocol: %v", err)
	}
	if p == nil {
		t.Fatal("expected non-nil protocol from NewProtocol")
	}
}

// TestNewProtocol_Error_OnMissingOptions: NewProtocol returns error on validation failure.
func TestNewProtocol_Error_OnMissingOptions(t *testing.T) {
	t.Parallel()
	_, err := ledger.NewProtocol() // zero options → error
	if err == nil {
		t.Fatal("expected error from NewProtocol when options missing")
	}
}

// TestWithChainHMAC_CallerSliceZeroedAfterCopy verifies the security invariant
// that WithChainHMAC zeroes the caller's key slice via clear(key) after copy.
// Removing the clear(key) call in WithChainHMAC body causes the "happy" subtest
// to fail: each key byte remains non-zero. Replaces TestProtocol_HMACKeyDefensiveCopy
// (HMACKey getter removed per A-08).
//
// Boundary cases (nil / short key) verify the documented fail-fast behavior:
// WithChainHMAC returns an error before clear() runs, and the caller slice is
// left untouched — no crash, no silent data alteration.
func TestWithChainHMAC_CallerSliceZeroedAfterCopy(t *testing.T) {
	t.Parallel()
	ns, _ := ledger.ParseNamespaceID("auditcore")

	tests := []struct {
		name       string
		keyLen     int // -1 → nil key
		wantErr    bool
		wantZeroed bool // post-call caller slice all-zero check
	}{
		{"happy_32_byte", 32, false, true},
		{"short_31_byte_rejected", 31, true, false}, // clear() never runs; caller slice untouched
		{"nil_key_rejected", -1, true, false},       // nil-safe: clear(nil) is no-op
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var key []byte
			if tt.keyLen >= 0 {
				key = make([]byte, tt.keyLen)
				for i := range key {
					key[i] = byte(i + 1)
				}
			}
			_, err := ledger.NewProtocol(
				ledger.WithChainHMAC(key),
				ledger.WithNamespace(ns),
				ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
				ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
			)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("NewProtocol: expected error for %s, got nil", tt.name)
				}
				if tt.keyLen > 0 {
					// Caller slice should be unchanged (no clear ran)
					if key[0] == 0 {
						t.Errorf("caller key unexpectedly zeroed on error path for %s; clear() must only run on success", tt.name)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("NewProtocol: %v", err)
			}
			if tt.wantZeroed {
				for i, b := range key {
					if b != 0 {
						t.Errorf("caller key not zeroed: first non-zero byte at index %d = %#x (additional non-zero bytes suppressed)", i, b)
						break // C.F2: surface first failure only
					}
				}
			}
		})
	}
}

// TestProtocol_Getters: Namespace / RestartRecovery / Idempotency return configured values.
func TestProtocol_Getters(t *testing.T) {
	t.Parallel()
	key := make([]byte, 32)
	ns, _ := ledger.ParseNamespaceID("auditcore")
	p, err := ledger.NewProtocol(
		ledger.WithChainHMAC(key),
		ledger.WithNamespace(ns),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	if err != nil {
		t.Fatalf("NewProtocol: %v", err)
	}
	if got := p.Namespace(); got != ns {
		t.Errorf("Namespace: got %q, want %q", got, ns)
	}
	if _, ok := p.RestartRecovery().(ledger.RestartRecoveryStrictTailVerify); !ok {
		t.Errorf("RestartRecovery: got %T, want RestartRecoveryStrictTailVerify", p.RestartRecovery())
	}
	if _, ok := p.Idempotency().(ledger.IdempotencyContentFingerprint); !ok {
		t.Errorf("Idempotency: got %T, want IdempotencyContentFingerprint", p.Idempotency())
	}
}
