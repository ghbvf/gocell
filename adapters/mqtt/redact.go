package mqtt

import (
	"net/url"

	"github.com/ghbvf/gocell/pkg/redaction"
)

// redactConnectURL strips credentials from a broker URL for logging.
//
// Two-layer compose (fail-closed): url.URL.Redacted() handles structural
// userinfo (e.g. "tcp://user:pass@host"), then pkg/redaction.RedactString
// handles free-form `key=value` sensitive substrings in query/fragment that
// net/url leaves intact (e.g. "?token=abc"). The compose is idempotent —
// both layers always run regardless of whether userinfo is present.
//
// ref: net/url.URL.Redacted() — golang/go src/net/url/url.go (userinfo only)
// ref: pkg/redaction single-source sensitivity list
func redactConnectURL(raw string) string {
	s := raw
	if u, err := url.Parse(raw); err == nil && u.User != nil {
		s = u.Redacted()
	}
	return redaction.RedactString(s)
}

// redactErr routes an error's text through pkg/redaction.RedactError (single source).
// nil → nil. When no substitution occurs, the original err is returned so
// errors.Is/As chains are preserved.
//
// ref: pkg/redaction.RedactError
func redactErr(err error) error {
	return redaction.RedactError(err)
}

// redactPayloadForLog routes payload bytes through pkg/redaction.RedactPayload.
// Sensitive JSON field values are replaced with "<REDACTED>"; malformed JSON
// is entirely replaced (fail-closed).
//
// ref: pkg/redaction.RedactPayload
func redactPayloadForLog(p []byte) []byte {
	return redaction.RedactPayload(p)
}
