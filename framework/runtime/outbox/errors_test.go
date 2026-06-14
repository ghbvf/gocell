package outbox

import (
	"strings"
	"testing"
)

// TestSanitizeError_RedactThenTruncateOrder verifies the operation order:
// redaction runs first, then truncation. This matters when a sensitive value
// would otherwise span the truncation boundary — redact-first guarantees the
// `<REDACTED>` token replaces the value cleanly, while truncate-first could
// chop the value mid-string and let the `<REDACTED>` boundary mismatch.
func TestSanitizeError_RedactThenTruncateOrder(t *testing.T) {
	t.Parallel()
	// 50 chars of preamble + sensitive token; truncate to 70 chars so the
	// raw value would have been cut, but the redacted form fits cleanly.
	preamble := strings.Repeat("x", 50)
	got := SanitizeError(preamble+" token=very-long-leak-sentinel-value-9f3", 70)
	if strings.Contains(got, "very-long-leak-sentinel-value-9f3") {
		t.Errorf("raw secret leaked through SanitizeError: %q", got)
	}
	if !strings.Contains(got, "<REDACTED>") {
		t.Errorf("redaction mask missing in SanitizeError output: %q", got)
	}
}

// TestSanitizeError_PassThroughWhenSafeAndShort verifies the no-op path:
// short, non-sensitive messages are returned essentially unchanged
// (modulo regex non-match → identity, truncate non-trigger → identity).
func TestSanitizeError_PassThroughWhenSafeAndShort(t *testing.T) {
	t.Parallel()
	const msg = "validation failed: field 'email' missing"
	if got := SanitizeError(msg, 1000); got != msg {
		t.Errorf("expected pass-through, got %q", got)
	}
}

// TestTruncateError_ZeroOrNegativeMaxIsNoOp documents that maxLen ≤ 0 is a
// no-op so callers passing an unbounded buffer flag (or arithmetic that
// underflows to a negative) get the input back rather than a panic.
func TestTruncateError_ZeroOrNegativeMaxIsNoOp(t *testing.T) {
	t.Parallel()
	const msg = "any message"
	if got := TruncateError(msg, 0); got != msg {
		t.Errorf("maxLen=0 must be no-op, got %q", got)
	}
	if got := TruncateError(msg, -1); got != msg {
		t.Errorf("maxLen<0 must be no-op, got %q", got)
	}
}

// TestSanitizeError_TruncateAtRuneBoundary verifies UTF-8 safety: an ASCII
// truncation should not split multi-byte characters. The redaction layer
// uses ASCII keys so it does not introduce any new multi-byte chars itself,
// but the residue (non-redacted Chinese context, etc.) must still truncate
// at rune boundaries.
func TestSanitizeError_TruncateAtRuneBoundary(t *testing.T) {
	t.Parallel()
	got := SanitizeError("错误消息测试用例", 4)
	if got != "错误消息" {
		t.Errorf("UTF-8 truncate not at rune boundary: %q", got)
	}
}
