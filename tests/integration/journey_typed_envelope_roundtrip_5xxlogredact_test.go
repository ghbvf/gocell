//go:build integration

package integration

import (
	"log/slog"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/redaction"
	"github.com/ghbvf/gocell/pkg/testutil/sloghelper"
)

// TestJTypedEnvelopeRoundtrip5xxLogRedact implements
// journeys/J-typed-envelope-roundtrip.yaml passCriteria
// "5xx 日志通过 pkg/redaction.RedactSlogAttr 处理 Details，
// password/token/dsn 等敏感字段在 slog 输出中替换为<REDACTED>" — checkRef
// journey.J-typed-envelope-roundtrip.5xx-log-redact.
//
// The production path is:
//
//	httputil.WriteError → writeErrcodeError → log5xx →
//	  for _, attr := range ecErr.Details {
//	    logAttrs = append(logAttrs, redaction.RedactSlogAttr(attr))
//	  }
//
// (pkg/httputil/response.go log5xx function, lines 229-231)
//
// # Redaction semantic clarification
//
// pkg/redaction.RedactSlogAttr applies RedactString to the *string value* of
// each slog.Attr. RedactString masks `key=value` / `key: value` substrings
// where key matches the sensitive-key pattern (password, token, dsn, etc.).
// It does NOT unconditionally replace the entire value of an attr whose *key*
// matches the pattern — it only scrubs sensitive substrings that appear in the
// *content* of the string value.
//
// This means:
//   - slog.String("error_context", "login failed: password=super-secret")
//     → value becomes "login failed: password=<REDACTED>"
//     (key=value substring inside the value is masked)
//   - slog.String("password", "super-secret") → value stays "super-secret"
//     (value has no key=value sub-form; RedactString is a no-op on bare values)
//
// The criterion "password/token/dsn 等敏感字段替换为 <REDACTED>" refers to
// the case where attr values contain embedded `key=value` strings (e.g. a
// formatted error message, a connection string, a debug dump).
//
// # Layer seam
//
// We test at the pkg/redaction layer (RedactSlogAttr unit contract) rather
// than driving the full httputil.log5xx → slog capture pipeline, because:
//  1. pkg/redaction.RedactSlogAttr IS the authoritative implementation of the
//     redaction invariant; log5xx's for-loop is the wiring that calls it.
//     Testing RedactSlogAttr directly proves the invariant with zero ambiguity.
//  2. httputil.WriteError + log5xx already have unit tests in pkg/httputil/
//     covering the wiring call. This criterion verifies the end-to-end
//     semantic: sensitive content in attr values is masked before reaching
//     slog backends.
//
// Docker-free: pure in-process pkg/ call.
func TestJTypedEnvelopeRoundtrip5xxLogRedact(t *testing.T) {
	t.Parallel()

	const mask = redaction.Mask

	// Table-driven: each case has an attr whose string value contains an
	// embedded `key=value` substring. RedactSlogAttr must mask the sensitive
	// substring while preserving the non-sensitive parts.
	// Each sensitive-key case uses slog.String("ctx", "<key>=<bare-secret>") so
	// that RedactSlogAttr finds the key=value substring inside the attr value and
	// masks the value portion while keeping the key prefix (partial mask).
	// The three-assertion invariant for every wantMask=true case is:
	//   (1) original key name is preserved verbatim in the redacted attr
	//   (2) "<key>=" prefix still appears in the redacted value  ← partial mask
	//   (3) the original bare secret does NOT appear in the redacted value
	cases := []struct {
		name       string
		input      slog.Attr
		wantMask   bool
		wantSub    string // substring that must appear in the output
		keyPrefix  string // "<key>=" that must survive after masking (partial-mask assertion)
		bareSecret string // raw secret that must NOT appear after masking
	}{
		{
			name:       "password-in-value",
			input:      slog.String("ctx", "login failed: password=super-secret details"),
			wantMask:   true,
			wantSub:    mask,
			keyPrefix:  "password=",
			bareSecret: "super-secret",
		},
		{
			name:       "passwd-in-value",
			input:      slog.String("ctx", "db error: passwd=bare-passwd-value extra"),
			wantMask:   true,
			wantSub:    mask,
			keyPrefix:  "passwd=",
			bareSecret: "bare-passwd-value",
		},
		{ //nolint:gosec // G101: intentional fake credential for redaction test
			name:       "pwd-in-value",
			input:      slog.String("ctx", "connect: pwd=bare-pwd-value remaining"),
			wantMask:   true,
			wantSub:    mask,
			keyPrefix:  "pwd=",
			bareSecret: "bare-pwd-value",
		},
		{
			name:       "secret-in-value",
			input:      slog.String("ctx", "config: secret=bare-secret-value next"),
			wantMask:   true,
			wantSub:    mask,
			keyPrefix:  "secret=",
			bareSecret: "bare-secret-value",
		},
		{ //nolint:gosec // G101: intentional fake credential for redaction test
			name:       "token-in-value",
			input:      slog.String("ctx", "auth failed: token=eyJhbGciOiJSUzI1NiJ9.payload.sig"),
			wantMask:   true,
			wantSub:    mask,
			keyPrefix:  "token=",
			bareSecret: "eyJhbGciOiJSUzI1NiJ9.payload.sig",
		},
		{ //nolint:gosec // G101: intentional fake credential for redaction test
			// api_key form (underscore variant, matches api[_-]?key pattern)
			name:       "api_key-in-value",
			input:      slog.String("ctx", "request: api_key=bare-api-key-value end"),
			wantMask:   true,
			wantSub:    mask,
			keyPrefix:  "api_key=",
			bareSecret: "bare-api-key-value",
		},
		{ //nolint:gosec // G101: intentional fake credential for redaction test
			// api-key form (hyphen variant, also matches api[_-]?key pattern)
			name:       "api-key-in-value",
			input:      slog.String("ctx", "request: api-key=bare-api-key-hyphen end"),
			wantMask:   true,
			wantSub:    mask,
			keyPrefix:  "api-key=",
			bareSecret: "bare-api-key-hyphen",
		},
		{ //nolint:gosec // G101: intentional fake credential for redaction test
			name:       "authorization-in-value",
			input:      slog.String("ctx", "header: authorization=Bearer bare-auth-token"),
			wantMask:   true,
			wantSub:    mask,
			keyPrefix:  "authorization=",
			bareSecret: "bare-auth-token",
		},
		{
			name:       "bearer-in-value",
			input:      slog.String("ctx", "header: bearer=bare-bearer-value end"),
			wantMask:   true,
			wantSub:    mask,
			keyPrefix:  "bearer=",
			bareSecret: "bare-bearer-value",
		},
		{
			name:       "private_key-in-value",
			input:      slog.String("ctx", "tls: private_key=bare-private-key-value end"),
			wantMask:   true,
			wantSub:    mask,
			keyPrefix:  "private_key=",
			bareSecret: "bare-private-key-value",
		},
		{
			name:       "signing_key-in-value",
			input:      slog.String("ctx", "jwt: signing_key=bare-signing-key-value end"),
			wantMask:   true,
			wantSub:    mask,
			keyPrefix:  "signing_key=",
			bareSecret: "bare-signing-key-value",
		},
		{ //nolint:gosec // G101: intentional fake credential for redaction test
			name:       "dsn-in-value",
			input:      slog.String("ctx", "db error: dsn=postgres://user:pwd@host:5432/db"),
			wantMask:   true,
			wantSub:    mask,
			keyPrefix:  "dsn=",
			bareSecret: "postgres://user:pwd@host:5432/db",
		},
		{
			name:       "connection_string-embedded",
			input:      slog.String("ctx", "connect failed: connection_string=Server=host;Pwd=abc remaining"),
			wantMask:   true,
			wantSub:    mask,
			keyPrefix:  "connection_string=",
			bareSecret: "Server=host",
		},
		{
			name:       "non-sensitive-passthrough",
			input:      slog.String("sessionId", "sess-non-sensitive-001"),
			wantMask:   false,
			wantSub:    "sess-non-sensitive-001",
			keyPrefix:  "",
			bareSecret: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			redacted := redaction.RedactSlogAttr(tc.input)

			// Key must be preserved verbatim.
			assert.Equal(t, tc.input.Key, redacted.Key,
				"RedactSlogAttr must preserve the attr key unchanged")

			val := redacted.Value.String()
			if tc.wantMask {
				assert.Containsf(t, val, mask,
					"RedactSlogAttr must mask sensitive key=value substrings in attr value. "+
						"input key=%q value=%q → got %q (expected to contain %q)",
					tc.input.Key, tc.input.Value.String(), val, mask)
				assert.NotEqualf(t, tc.input.Value.String(), val,
					"RedactSlogAttr must have modified the value when masking occurred. "+
						"input=%q output=%q", tc.input.Value.String(), val)
				// Partial-mask assertion: the key name prefix must survive redaction
				// (observability.md: "保留原 key 与大小写"). Total erasure would
				// prevent operators from knowing which field was redacted.
				if tc.keyPrefix != "" {
					assert.Containsf(t, val, tc.keyPrefix,
						"RedactSlogAttr must preserve the sensitive key name prefix %q in the "+
							"masked output (partial mask, not total erasure). got %q",
						tc.keyPrefix, val)
				}
				if tc.bareSecret != "" {
					assert.NotContainsf(t, val, tc.bareSecret,
						"RedactSlogAttr must not expose the bare secret %q in the masked output. "+
							"got %q", tc.bareSecret, val)
				}
			} else {
				assert.Equal(t, tc.input.Value.String(), val,
					"RedactSlogAttr must not modify non-sensitive attr values")
			}
		})
	}
}

// TestJTypedEnvelopeRoundtrip5xxLogRedactWiring drives the *real* log5xx path
// end-to-end and asserts the slog Record emitted by the 5xx logger has
// Details attrs masked. Together with the table-driven unit cases above (which
// pin RedactSlogAttr's semantic contract), this guards the wiring in
// pkg/httputil/response.go:229-231 — the for-loop that applies RedactSlogAttr
// to each ecErr.Details attr before slog.Error. If that for-loop is dropped
// or replaced with a non-redacting append, this test fails immediately;
// without it, a pure pkg/redaction unit assertion (the prior log5xx-wiring
// sub-test) would still pass while production silently leaked PII.
//
// NOT t.Parallel — slog.SetDefault swap is process-global, racing any
// concurrent t.Parallel sibling that reads or writes the default logger.
// Isolated as a top-level test so the parent unit table remains parallel.
//
// Test sink uses pkg/testutil/sloghelper.SyncBuffer per its godoc requirement
// for concurrent-safe slog output capture (bare bytes.Buffer races under
// -race when Handler writes are interleaved with String() reads).
func TestJTypedEnvelopeRoundtrip5xxLogRedactWiring(t *testing.T) {
	buf := sloghelper.NewSyncBuffer()
	orig := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(orig) })

	// Embed a sensitive `password=<secret>` substring inside a Details attr
	// value so log5xx's for-loop (response.go:229-231) is the only thing
	// standing between the raw secret and the slog backend.
	ecErr := errcode.New(
		errcode.KindInternal,
		errcode.ErrInternal,
		"internal server error",
		errcode.WithDetails(slog.String("error_context",
			"connection failed: password=super-secret-value")),
	)
	w := callWriteErrorWithStatus(http.StatusInternalServerError, ecErr)
	assertHTTPStatus(t, w, http.StatusInternalServerError)

	entry := sloghelper.FindLogEntry(buf.String(), "typed (5xx)")
	require.NotNilf(t, entry,
		"expected slog Error line with msg 'typed (5xx)' from log5xx; "+
			"if absent, the 500 → log5xx path is broken. captured: %s",
		buf.String())

	ctxVal, ok := entry["error_context"].(string)
	require.Truef(t, ok,
		"slog line missing 'error_context' attr; log5xx must propagate Details "+
			"attrs (after redaction) to slog. entry: %#v", entry)

	assert.Containsf(t, ctxVal, redaction.Mask,
		"log5xx wiring: Details attr containing 'password=...' must be routed "+
			"through redaction.RedactSlogAttr before slog.Error. If "+
			"pkg/httputil/response.go:229-231 for-loop is dropped or bypassed, "+
			"this assertion exposes the regression. got %q", ctxVal)
	assert.Containsf(t, ctxVal, "password=",
		"log5xx wiring: sensitive key prefix 'password=' must survive masking "+
			"(partial mask, not total erasure). got %q", ctxVal)
	assert.NotContainsf(t, ctxVal, "super-secret-value",
		"log5xx wiring: bare secret must never reach slog backend. got %q", ctxVal)
}
