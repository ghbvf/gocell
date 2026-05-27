package mqtt

import (
	"net/url"

	"github.com/ghbvf/gocell/pkg/redaction"
)

// redactConnectURL strips credentials from a broker URL for logging.
//
// When the URL is parseable by net/url, url.URL.Redacted() replaces the
// userinfo with "xxxxx" in accordance with net/url's built-in credential
// masking. For unparseable input, falls back to pkg/redaction.RedactString
// so free-form `key=value` sensitive substrings are still masked.
//
// ref: net/url.URL.Redacted() — golang/go src/net/url/url.go
// ref: pkg/redaction single-source sensitivity list
func redactConnectURL(raw string) string {
	u, err := url.Parse(raw)
	if err == nil && u.User != nil {
		return u.Redacted()
	}
	return redaction.RedactString(raw)
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
