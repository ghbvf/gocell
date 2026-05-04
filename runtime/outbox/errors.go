package outbox

import "github.com/ghbvf/gocell/pkg/redaction"

// ---------------------------------------------------------------------------
// Error sanitization helpers
// ---------------------------------------------------------------------------
// Shared by runtime/outbox (relay) and adapters/postgres (outbox_store).
// Delegates the actual scrub + truncate work to pkg/redaction so the regex
// (sensitive key set, value-boundary handling) is single-sourced with
// kernel/wrapper's hardcoded fail-closed redactor. ref: pkg/redaction.

// TruncateError truncates an error message to maxLen runes, preserving valid
// UTF-8. Negative or zero maxLen is a no-op.
func TruncateError(msg string, maxLen int) string {
	return redaction.TruncateString(msg, maxLen)
}

// SanitizeError redacts sensitive substrings then truncates to maxLen runes
// before storing in a last_error column.
func SanitizeError(errMsg string, maxLen int) string {
	return redaction.TruncateString(redaction.RedactString(errMsg), maxLen)
}
