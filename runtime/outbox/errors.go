package outbox

import (
	"regexp"
	"unicode/utf8"
)

// ---------------------------------------------------------------------------
// Error sanitization helpers
// ---------------------------------------------------------------------------
// Shared by runtime/outbox (relay) and adapters/postgres (outbox_store).
// Exported so that adapters can call outbox.SanitizeError without duplicating
// the regexp and truncation logic.

// sensitivePatterns matches common sensitive substrings in error messages
// (connection strings, hostnames, credentials) to redact before storage.
var sensitivePatterns = regexp.MustCompile(
	`(?i)(password|passwd|secret|token|dsn|connection[_ ]?string)=[^\s;,]+`,
)

// TruncateError truncates an error message to maxLen runes, preserving valid
// UTF-8 (avoids splitting multi-byte characters at byte boundaries).
func TruncateError(msg string, maxLen int) string {
	if utf8.RuneCountInString(msg) <= maxLen {
		return msg
	}
	runes := []rune(msg)
	return string(runes[:maxLen])
}

// SanitizeError truncates and redacts sensitive patterns from an error message
// before storing it in a last_error column.
func SanitizeError(errMsg string, maxLen int) string {
	redacted := sensitivePatterns.ReplaceAllString(errMsg, "$1=<REDACTED>")
	return TruncateError(redacted, maxLen)
}
