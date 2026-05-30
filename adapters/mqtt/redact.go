package mqtt

import (
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/ghbvf/gocell/pkg/redaction"
)

// maxTopicLogLen caps the rune length of a broker-delivered topic before it is
// written to a log line. A hostile producer could craft an arbitrarily long
// topic; capping bounds log-line size independent of the redaction pass.
const maxTopicLogLen = 256

// safeTopicForLog sanitizes a broker-delivered (untrusted) MQTT topic before it
// is logged. pb.Topic is wire bytes the broker forwards verbatim; unlike
// entry.Topic (which UnmarshalEnvelope validates via idutil.SafeID), the raw
// topic on the intake-stop drop / unmarshal-poison / ackPoison paths is never
// validated. Logging it directly is a CWE-117 log-injection sink: a CR/LF or
// ANSI escape embedded in the topic could forge log lines or corrupt terminal
// output.
//
// Two-layer fail-closed (mirrors the redact.go redactConnectURL compose):
//  1. strip control characters (CR, LF, tab, ANSI ESC, other non-printables) so
//     no line break / escape sequence survives into the log;
//  2. route the residue through pkg/redaction.RedactString so any sensitive
//     key=value substring in the topic is masked (single-source sensitivity
//     list), then cap to maxTopicLogLen runes.
//
// ref: pkg/redaction single-source sensitivity list; idutil.SafeID rationale.
func safeTopicForLog(topic string) string {
	stripped := strings.Map(func(r rune) rune {
		if r == unicode.ReplacementChar {
			return -1
		}
		if unicode.IsControl(r) || !strconv.IsPrint(r) {
			return -1
		}
		return r
	}, topic)
	return redaction.TruncateString(redaction.RedactString(stripped), maxTopicLogLen)
}

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

// safeErrForLog sanitizes an error's text for slog the SAME way safeTopicForLog
// sanitizes a topic: strip control / non-printable runes, mask key=value
// secrets, then truncate. Use when the error may embed untrusted input — e.g. an
// errcode whose Internal details carry a broker-delivered topic (errcode.Error()
// renders internal details as "topic=<raw>"). redactErr alone masks secrets but
// does NOT strip control chars, so a topic with embedded newlines would still be
// a CWE-117 log-injection vector. nil → "".
func safeErrForLog(err error) string {
	if err == nil {
		return ""
	}
	return safeTopicForLog(err.Error())
}
