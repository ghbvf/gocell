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

// Note: TestNewProtocol_NoOptions_Error was retired with the positional-
// argument refactor. `ledger.NewProtocol()` with no arguments is a Go
// compile-time error now (namespace + key are required positional params),
// so the runtime-error contract this test asserted has been replaced by the
// type system itself. See AUDIT-HASH-INPUT-FROZEN-01 godoc for the funnel
// shape.

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
		ns,
		hmacKey,
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
		ns,
		key,
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
		ns,
		hmacKey,
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
		ns,
		hmacKey,
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
		ns,
		nil,
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	if err == nil {
		t.Fatal("expected error for nil HMAC key")
	}
}

// TestNewProtocol_EmptyNamespace_Rejected: zero-value namespace is rejected at
// the positional-arg validation step.
func TestNewProtocol_EmptyNamespace_Rejected(t *testing.T) {
	t.Parallel()
	hmacKey := make([]byte, 32)
	_, err := ledger.NewProtocol(
		"",
		hmacKey,
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	if err == nil {
		t.Fatal("expected error for empty namespace")
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
// options. Two former cases — WithChainHMAC(nil) and WithNamespace("") —
// retired with the positional-arg refactor: the underlying Option funcs were
// deleted because the values they used to wire are now mandatory positional
// arguments, which makes "called with nil/empty" a runtime check against the
// `key []byte` / `namespace NamespaceID` parameters themselves (covered by
// TestNewProtocol_WithHMACNil_Rejected + TestNewProtocol_EmptyNamespace_Rejected).
// ---------------------------------------------------------------------------

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
		ns,
		validKey,
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
		ns,
		validKey,
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
		ns,
		hmacKey,
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

// Note: TestNewProtocol_Error_OnMissingOptions was retired with the positional-
// argument refactor. `ledger.NewProtocol()` with no arguments is a Go compile-
// time error now (namespace + key are required), so the runtime-error contract
// this test exercised has been replaced by the type system.

// TestWithChainHMAC_CallerSliceZeroedAfterCopy verifies the security invariant
// that WithChainHMAC zeroes the caller's key slice via clear(key) after copy.
// Removing the clear(key) call in WithChainHMAC body causes the "happy" subtest
// to fail: each key byte remains non-zero. Replaces TestProtocol_HMACKeyDefensiveCopy
// (HMACKey getter removed per A-08).
//
// Boundary cases (nil / short key) verify the documented fail-fast behavior:
// WithChainHMAC returns an error before clear() runs, and the caller slice is
// left untouched — no crash, no silent data alteration.
type chainHMACKeyCase struct {
	name       string
	keyLen     int  // -1 → nil key
	wantErr    bool
	wantZeroed bool // post-call caller slice all-zero check
}

func runChainHMACKeyCase(t *testing.T, ns ledger.NamespaceID, tc chainHMACKeyCase) {
	t.Helper()
	var key []byte
	if tc.keyLen >= 0 {
		key = make([]byte, tc.keyLen)
		for i := range key {
			key[i] = byte(i + 1)
		}
	}
	_, err := ledger.NewProtocol(
		ns,
		key,
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	if tc.wantErr {
		if err == nil {
			t.Fatalf("NewProtocol: expected error for %s, got nil", tc.name)
		}
		if tc.keyLen > 0 && key[0] == 0 {
			// Caller slice should be unchanged (no clear ran)
			t.Errorf("caller key unexpectedly zeroed on error path for %s; clear() must only run on success", tc.name)
		}
		return
	}
	if err != nil {
		t.Fatalf("NewProtocol: %v", err)
	}
	if !tc.wantZeroed {
		return
	}
	for i, b := range key {
		if b != 0 {
			t.Errorf("caller key not zeroed: first non-zero byte at index %d = %#x (additional non-zero bytes suppressed)", i, b)
			break // C.F2: surface first failure only
		}
	}
}

func TestWithChainHMAC_CallerSliceZeroedAfterCopy(t *testing.T) {
	t.Parallel()
	ns, _ := ledger.ParseNamespaceID("auditcore")

	tests := []chainHMACKeyCase{
		{"happy_32_byte", 32, false, true},
		{"short_31_byte_rejected", 31, true, false}, // clear() never runs; caller slice untouched
		{"nil_key_rejected", -1, true, false},       // nil-safe: clear(nil) is no-op
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runChainHMACKeyCase(t, ns, tt)
		})
	}
}

// TestProtocol_Getters: Namespace / RestartRecovery / Idempotency return configured values.
func TestProtocol_Getters(t *testing.T) {
	t.Parallel()
	key := make([]byte, 32)
	ns, _ := ledger.ParseNamespaceID("auditcore")
	p, err := ledger.NewProtocol(
		ns,
		key,
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
