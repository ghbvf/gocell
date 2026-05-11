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
			key := make([]byte, tc.keyLen)
			_, err := ledger.NewProtocol(
				ledger.WithChainHMAC(key),
				ledger.WithNamespace(ns),
				ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
				ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
			)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for key length %d, got nil", tc.keyLen)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error for key length %d: %v", tc.keyLen, err)
			}
			if tc.wantErr && err != nil {
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
// A-06 RED: With* Option nil → immediate error (short-circuit), not sticky sentinel.
//
// Current implementation uses a sentinel flag (hmacKeyNil / restartRecoveryNil /
// idempotencyNil) that is checked only at the end of NewProtocol. The target
// semantics require the Option func itself to return an error immediately so that
// NewProtocol short-circuits and does NOT execute subsequent options.
//
// We test this by inserting a side-effect "probe" option after the nil option:
//   - RED (current): probe runs because nil option returns nil error
//   - GREEN (after fix): probe does NOT run because nil option returns error → short-circuit
// ---------------------------------------------------------------------------

// TestWithChainHMAC_NilReturnsError_Immediate verifies that WithChainHMAC(nil)
// causes NewProtocol to short-circuit immediately, not execute subsequent options.
//
// A-06 RED: current WithChainHMAC(nil) returns nil error (sets hmacKeyNil sentinel).
// The sentinel-sticky doctrine means the nil option returns nil, subsequent options
// still run, and the error is only detected at the END of NewProtocol.
//
// After A-06 fix: the Option itself must return a non-nil error immediately,
// making NewProtocol stop applying further options. The observable: passing
// WithChainHMAC(nil) then WithChainHMAC(validKey) must still fail — and the
// error message must indicate "nil/empty key" (immediate error), not the deferred
// "HMAC key required (use WithChainHMAC)" sentinel message.
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
		t.Fatal("A-06: WithChainHMAC(nil) followed by valid key must still return error")
	}
	errStr := err.Error()
	// RED assertion: current deferred sentinel message is "HMAC key required (use WithChainHMAC".
	// GREEN: immediate error mentions "nil" or "empty" (not the deferred sentinel).
	// This FAILS in RED state because the sentinel message does not contain "nil" or "empty".
	if !containsAny(errStr, "nil", "empty", "missing key") {
		t.Errorf("A-06 RED: WithChainHMAC(nil) should immediately return error "+
			"mentioning 'nil' or 'empty', got deferred sentinel message: %q", errStr)
	}
}

// TestWithNamespace_EmptyReturnsError_Immediate verifies that WithNamespace("")
// causes NewProtocol to short-circuit immediately.
//
// A-06 RED: current WithNamespace("") sets namespaceNil sentinel (deferred check).
func TestWithNamespace_EmptyReturnsError_Immediate(t *testing.T) {
	t.Parallel()
	validKey := make([]byte, 32)
	for i := range validKey {
		validKey[i] = byte(i + 1)
	}
	validNS, _ := ledger.ParseNamespaceID("auditcore")

	// Passing empty namespace then valid namespace: target semantics = short-circuit,
	// valid NS option never runs → protocol is nil.
	// Current semantics = sentinel-sticky → error at end even after valid NS is set.
	_, err := ledger.NewProtocol(
		ledger.WithChainHMAC(validKey),
		ledger.WithNamespace(""), // empty NamespaceID = typed zero value
		ledger.WithNamespace(validNS),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	if err == nil {
		t.Fatal("A-06: WithNamespace(\"\") followed by valid namespace must still return error")
	}
	errStr := err.Error()
	// A-06 RED: current error is the deferred "namespace required" sentinel message.
	// GREEN: error is immediate and mentions "empty" or the zero-value namespace specifically.
	if !containsAny(errStr, "empty", "must not be empty", "namespace ID must not be empty") {
		t.Errorf("A-06 RED: WithNamespace(\"\") should immediately return error "+
			"mentioning empty namespace, got deferred sentinel: %q", errStr)
	}
}

// TestWithRestartRecovery_NilReturnsError_Immediate verifies that
// WithRestartRecovery(nil) causes NewProtocol to short-circuit immediately.
//
// A-06 RED: current implementation sets restartRecoveryNil sentinel (deferred).
func TestWithRestartRecovery_NilReturnsError_Immediate(t *testing.T) {
	t.Parallel()
	validKey := make([]byte, 32)
	for i := range validKey {
		validKey[i] = byte(i + 1)
	}
	ns, _ := ledger.ParseNamespaceID("auditcore")
	var nilRR ledger.RestartRecoveryMode // typed nil

	// nil then valid: target = short-circuit; current = sentinel-sticky error at end.
	_, err := ledger.NewProtocol(
		ledger.WithChainHMAC(validKey),
		ledger.WithNamespace(ns),
		ledger.WithRestartRecovery(nilRR),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	if err == nil {
		t.Fatal("A-06: WithRestartRecovery(nil) followed by valid mode must still return error")
	}
	errStr := err.Error()
	// A-06 RED: current error is the deferred sentinel message.
	// GREEN: error comes from WithRestartRecovery option immediately and mentions
	// nil/invalid directly rather than "restart recovery mode required".
	if !containsAny(errStr, "nil", "invalid", "must not be nil") {
		t.Errorf("A-06 RED: WithRestartRecovery(nil) should immediately error "+
			"mentioning nil/invalid, got deferred sentinel: %q", errStr)
	}
}

// TestWithIdempotency_NilReturnsError_Immediate verifies that
// WithIdempotency(nil) causes NewProtocol to short-circuit immediately.
//
// A-06 RED: current implementation sets idempotencyNil sentinel (deferred).
func TestWithIdempotency_NilReturnsError_Immediate(t *testing.T) {
	t.Parallel()
	validKey := make([]byte, 32)
	for i := range validKey {
		validKey[i] = byte(i + 1)
	}
	ns, _ := ledger.ParseNamespaceID("auditcore")
	var nilIM ledger.IdempotencyMode // typed nil

	// nil then valid: target = short-circuit; current = sentinel-sticky error at end.
	_, err := ledger.NewProtocol(
		ledger.WithChainHMAC(validKey),
		ledger.WithNamespace(ns),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(nilIM),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	if err == nil {
		t.Fatal("A-06: WithIdempotency(nil) followed by valid mode must still return error")
	}
	errStr := err.Error()
	// A-06 RED: current error is the deferred sentinel message.
	// GREEN: error comes from WithIdempotency option immediately and mentions nil.
	if !containsAny(errStr, "nil", "invalid", "must not be nil") {
		t.Errorf("A-06 RED: WithIdempotency(nil) should immediately error "+
			"mentioning nil/invalid, got deferred sentinel: %q", errStr)
	}
}

// containsAny returns true if s contains any of the given substrings.
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// TestMustNewProtocol_OK: composition-root convenience wrapper succeeds with valid options.
func TestMustNewProtocol_OK(t *testing.T) {
	t.Parallel()
	hmacKey := make([]byte, 32)
	for i := range hmacKey {
		hmacKey[i] = byte(i + 1)
	}
	ns, _ := ledger.ParseNamespaceID("auditcore")
	p := ledger.MustNewProtocol(
		ledger.WithChainHMAC(hmacKey),
		ledger.WithNamespace(ns),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	if p == nil {
		t.Fatal("expected non-nil protocol from MustNewProtocol")
	}
}

// TestMustNewProtocol_Panic_OnError: MustNewProtocol panics on validation failure.
func TestMustNewProtocol_Panic_OnError(t *testing.T) {
	t.Parallel()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic from MustNewProtocol when options missing")
		}
		err, ok := r.(error)
		if !ok {
			t.Fatalf("expected panic value to be error, got %T: %v", r, r)
		}
		if err == nil {
			t.Fatal("panic error must be non-nil")
		}
	}()
	_ = ledger.MustNewProtocol() // zero options → panic
}

// TestProtocol_HMACKeyDefensiveCopy: HMACKey() returns a defensive copy.
func TestProtocol_HMACKeyDefensiveCopy(t *testing.T) {
	t.Parallel()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	ns, _ := ledger.ParseNamespaceID("auditcore")
	p := ledger.MustNewProtocol(
		ledger.WithChainHMAC(key),
		ledger.WithNamespace(ns),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	got := p.HMACKey()
	if len(got) != len(key) {
		t.Fatalf("HMACKey length: got %d, want %d", len(got), len(key))
	}
	// Mutate the returned copy.
	got[0] = 0xFF
	again := p.HMACKey()
	if again[0] == 0xFF {
		t.Error("HMACKey() must return a defensive copy; caller mutation leaked into protocol")
	}
}

// TestProtocol_Getters: Namespace / RestartRecovery / Idempotency return configured values.
func TestProtocol_Getters(t *testing.T) {
	t.Parallel()
	key := make([]byte, 32)
	ns, _ := ledger.ParseNamespaceID("auditcore")
	p := ledger.MustNewProtocol(
		ledger.WithChainHMAC(key),
		ledger.WithNamespace(ns),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
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
