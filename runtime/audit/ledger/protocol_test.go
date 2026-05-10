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

// TestNewProtocol_RestartRecoveryNilSticky: once typed-nil is observed, a
// subsequent valid call does NOT clear the sentinel (sentinel sticky doctrine).
func TestNewProtocol_RestartRecoveryNilSticky(t *testing.T) {
	t.Parallel()
	hmacKey := make([]byte, 32)
	ns, _ := ledger.ParseNamespaceID("auditcore")
	var nilRR ledger.RestartRecoveryMode // typed nil
	_, err := ledger.NewProtocol(
		ledger.WithChainHMAC(hmacKey),
		ledger.WithNamespace(ns),
		ledger.WithRestartRecovery(nilRR),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}), // valid after nil
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	if err == nil {
		t.Fatal("expected error: typed-nil sentinel must be sticky")
	}
}

// TestNewProtocol_IdempotencyNilSticky: same for IdempotencyMode sentinel.
func TestNewProtocol_IdempotencyNilSticky(t *testing.T) {
	t.Parallel()
	hmacKey := make([]byte, 32)
	ns, _ := ledger.ParseNamespaceID("auditcore")
	var nilIM ledger.IdempotencyMode // typed nil
	_, err := ledger.NewProtocol(
		ledger.WithChainHMAC(hmacKey),
		ledger.WithNamespace(ns),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(nilIM),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}), // valid after nil
	)
	if err == nil {
		t.Fatal("expected error: typed-nil sentinel must be sticky for IdempotencyMode")
	}
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
