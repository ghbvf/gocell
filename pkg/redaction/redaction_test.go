package redaction_test

import (
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/pkg/redaction"
)

func TestRedactString(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "password_keyEqValue",
			in:   "login failed: password=hunter2 user=alice",
			want: "login failed: password=<REDACTED> user=alice",
		},
		{
			name: "passwd_alias",
			in:   "passwd=secret123 something",
			want: "passwd=<REDACTED> something",
		},
		{
			// pwd is a known false-positive trigger: a log emitting
			// `pwd=/home/user` (working dir) gets masked. This is the
			// intentional fail-closed cost — masking a directory path is
			// preferable to leaking SQL Server `Pwd=secret`. Documented
			// behavior, not a bug. dev workflows needing raw working dir
			// should use slog structured fields instead.
			name: "pwd_workdir_falsePositive_documented",
			in:   "starting worker pwd=/home/user/app",
			want: "starting worker pwd=<REDACTED>",
		},
		{
			name: "token_keyEqValue",
			in:   "upstream 401: token=eyJhbGc.foo",
			want: "upstream 401: token=<REDACTED>",
		},
		{
			name: "accessToken_camel",
			in:   "oauth exchange failed: accessToken=access-value",
			want: "oauth exchange failed: accessToken=<REDACTED>",
		},
		{
			name: "refreshToken_camel",
			in:   "oauth exchange failed: refreshToken=1//0g",
			want: "oauth exchange failed: refreshToken=<REDACTED>",
		},
		{
			name: "access_token_snake",
			in:   "oauth exchange failed: access_token=access-value",
			want: "oauth exchange failed: access_token=<REDACTED>",
		},
		{
			name: "refresh_token_snake",
			in:   "oauth exchange failed: refresh_token=1//0g",
			want: "oauth exchange failed: refresh_token=<REDACTED>",
		},
		{
			name: "id_token_snake",
			in:   "oauth exchange failed: id_token=eyJhbGc",
			want: "oauth exchange failed: id_token=<REDACTED>",
		},
		{
			name: "authorization_colonSpace",
			in:   "Authorization: Bearer abc.def.ghi",
			want: "Authorization: <REDACTED>",
		},
		{
			// authorizationPattern stops at end-of-line, NOT at `;`. A `;`
			// inside an opaque bearer token must not leak the suffix —
			// over-masking the same-line `trace_id=1` is the accepted
			// fail-closed cost.
			name: "authorization_semicolonInValue_noLeak",
			in:   "Authorization: Bearer abc.def.ghi; trace_id=1",
			want: "Authorization: <REDACTED>",
		},
		{
			// Multi-line: only the Authorization line is masked; the next
			// header line survives because the boundary is `\n`.
			name: "authorization_newlineBoundary",
			in:   "Authorization: Bearer abc.def.ghi\nContent-Type: json",
			want: "Authorization: <REDACTED>\nContent-Type: json",
		},
		{
			name: "bearer_keyEqValue",
			in:   "bearer=abc.def.ghi end",
			want: "bearer=<REDACTED> end",
		},
		{
			name: "secret_simple",
			in:   "secret=topsecret",
			want: "secret=<REDACTED>",
		},
		{
			// dsn= field redaction: the entire URL value is masked. The
			// fixture uses a credential-free URL so gosec G101 does not flag
			// the literal; the redaction itself does not depend on whether
			// user:pass is embedded — the `dsn=` key is the trigger.
			name: "dsn_postgres",
			in:   "connect failed: dsn=postgres://h/db?sslmode=require trailing",
			want: "connect failed: dsn=<REDACTED> trailing",
		},
		{
			name: "connection_string_underscore",
			in:   "connection_string=Server=foo;Pwd=bar",
			want: "connection_string=<REDACTED>",
		},
		{
			// connectionStringPattern stops at whitespace so trailing log
			// context survives. Verifies boundary stays at ` ` not `;`.
			name: "connection_string_whitespaceBoundary",
			in:   "connection_string=Server=foo;Pwd=bar trailing_ctx=ok",
			want: "connection_string=<REDACTED> trailing_ctx=ok",
		},
		{
			name: "connection_space",
			in:   "connection string=somevalue trailing",
			want: "connection string=<REDACTED> trailing",
		},
		{
			name: "apiKey_camel",
			in:   "apikey=abc123",
			want: "apikey=<REDACTED>",
		},
		{
			name: "apiKey_underscore",
			in:   "api_key=abc123",
			want: "api_key=<REDACTED>",
		},
		{
			name: "apiKey_hyphen",
			in:   "api-key=abc123",
			want: "api-key=<REDACTED>",
		},
		{
			name: "privateKey_underscore",
			in:   "private_key=MIIEvQIBADANBg",
			want: "private_key=<REDACTED>",
		},
		{
			name: "privateKey_hyphen",
			in:   "private-key=MIIEvQ",
			want: "private-key=<REDACTED>",
		},
		{
			name: "signing_key",
			in:   "signing_key=topsecret",
			want: "signing_key=<REDACTED>",
		},
		{
			name: "caseInsensitive_upper",
			in:   "PASSWORD=abc",
			want: "PASSWORD=<REDACTED>",
		},
		{
			name: "caseInsensitive_mixed",
			in:   "Token=xyz",
			want: "Token=<REDACTED>",
		},
		{
			name: "multipleKeys",
			in:   "password=a token=b",
			want: "password=<REDACTED> token=<REDACTED>",
		},
		{
			// fail-closed: a `,` inside the secret value must NOT terminate
			// the redacted span — otherwise `def` (the suffix of the
			// original secret) would leak past the mask.
			name: "password_commaInValue_noLeak",
			in:   "password=abc,def next=ok",
			want: "password=<REDACTED> next=ok",
		},
		{
			// fail-closed: same for `;` inside the value.
			name: "token_semicolonInValue_noLeak",
			in:   "token=abc;def next=ok",
			want: "token=<REDACTED> next=ok",
		},
		{
			// Documents the over-mask trade-off: when a key=value field is
			// followed by `,key2=value2` with no whitespace between, the
			// fail-closed `\S+` boundary swallows both fields. This is the
			// accepted cost — losing user="alice" context here is cheaper
			// than risking a `,`-suffixed secret leaking past the mask.
			// Callers needing the trailing field intact must emit a space
			// between fields, or use slog structured fields instead.
			name: "commaSeparatedFields_overMask_documented",
			in:   `password="abc",user="alice"`,
			want: `password=<REDACTED>`,
		},
		{
			name: "json_password_quoted",
			in:   `{"password":"hunter2","user":"alice"}`,
			want: `{"password":"<REDACTED>","user":"alice"}`,
		},
		{
			name: "json_token_quoted_with_spaces",
			in:   `{"token" : "abc.def.ghi","ok":true}`,
			want: `{"token" : "<REDACTED>","ok":true}`,
		},
		{
			name: "json_accessToken_quoted",
			in:   `{"accessToken":"access-value","user":"alice"}`,
			want: `{"accessToken":"<REDACTED>","user":"alice"}`,
		},
		{
			name: "json_refreshToken_quoted",
			in:   `{"refreshToken":"1//0g","user":"alice"}`,
			want: `{"refreshToken":"<REDACTED>","user":"alice"}`,
		},
		{
			name: "json_access_token_quoted",
			in:   `{"access_token":"access-value","user":"alice"}`,
			want: `{"access_token":"<REDACTED>","user":"alice"}`,
		},
		{
			name: "json_refresh_token_quoted",
			in:   `{"refresh_token":"1//0g","user":"alice"}`,
			want: `{"refresh_token":"<REDACTED>","user":"alice"}`,
		},
		{
			name: "json_id_token_quoted",
			in:   `{"id_token":"eyJhbGc","user":"alice"}`,
			want: `{"id_token":"<REDACTED>","user":"alice"}`,
		},
		{
			name: "json_authorization_quoted",
			in:   `{"authorization":"Bearer abc.def.ghi","user":"alice"}`,
			want: `{"authorization":"<REDACTED>","user":"alice"}`,
		},
		{
			name: "json_connection_string_quoted",
			in:   `{"connection_string":"Server=foo;Pwd=bar","ok":true}`,
			want: `{"connection_string":"<REDACTED>","ok":true}`,
		},
		{
			name: "json_secret_escaped_quote",
			in:   `{"secret":"abc\"def","user":"alice"}`,
			want: `{"secret":"<REDACTED>","user":"alice"}`,
		},
		{
			name: "noMatch_passthrough",
			in:   "plain validation error: field foo missing",
			want: "plain validation error: field foo missing",
		},
		{
			name: "webhook_secret_keyEqValue",
			in:   "config rejected: webhook_secret=whsec_abc123 source=stripe",
			want: "config rejected: webhook_secret=<REDACTED> source=stripe",
		},
		{
			name: "x_signature_json",
			in:   `{"x-signature":"v1,abc==","deliveryId":"d-1"}`,
			want: `{"x-signature":"<REDACTED>","deliveryId":"d-1"}`,
		},
		{
			name: "svix_signature_keyColon",
			in:   "verify failed: svix_signature: v1,deadbeef",
			want: "verify failed: svix_signature: <REDACTED>",
		},
		{
			name: "hmac_key_keyEqValue",
			in:   "signer init: hmac_key=supersecret done",
			want: "signer init: hmac_key=<REDACTED> done",
		},
		{
			name: "stripe_sig_kv",
			in:   "stripe_signature=abc next=ok",
			want: "stripe_signature=<REDACTED> next=ok",
		},
		{
			name: "x_hub_signature_256_kv",
			in:   "x_hub_signature_256=abc trailing",
			want: "x_hub_signature_256=<REDACTED> trailing",
		},
		{
			name: "x_hub_signature_1_kv",
			in:   "x-hub-signature-1=abc trailing",
			want: "x-hub-signature-1=<REDACTED> trailing",
		},
		{
			name: "empty",
			in:   "",
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := redaction.RedactString(tc.in)
			if got != tc.want {
				t.Errorf("RedactString(%q):\n  got:  %q\n  want: %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRedactString_DoesNotLeakSensitiveValue(t *testing.T) {
	t.Parallel()
	const secret = "hunter2-very-unique-token"
	out := redaction.RedactString("login: password=" + secret + " end")
	if strings.Contains(out, secret) {
		t.Errorf("redacted output still contains secret value: %q", out)
	}

	jsonOut := redaction.RedactString(`{"token":"` + secret + `","user":"alice"}`)
	if strings.Contains(jsonOut, secret) {
		t.Errorf("redacted JSON output still contains secret value: %q", jsonOut)
	}
}

func TestRedactError(t *testing.T) {
	t.Parallel()

	t.Run("nil_in_nil_out", func(t *testing.T) {
		t.Parallel()
		if got := redaction.RedactError(nil); got != nil {
			t.Errorf("RedactError(nil) = %v, want nil", got)
		}
	})

	t.Run("noChange_returnsSameInstance", func(t *testing.T) {
		t.Parallel()
		err := errors.New("plain validation error")
		got := redaction.RedactError(err)
		assertSameErrorIdentity(t, got, err, "identity must be preserved when nothing changes")
	})

	t.Run("redacted_returnsNewInstance", func(t *testing.T) {
		t.Parallel()
		original := errors.New("password=hunter2")
		got := redaction.RedactError(original)
		assertDifferentErrorIdentity(t, got, original, "expected a new error with masked text")
		if got.Error() != "password=<REDACTED>" {
			t.Errorf("redacted msg = %q, want %q", got.Error(), "password=<REDACTED>")
		}
	})

	t.Run("redacted_doesNotLeak", func(t *testing.T) {
		t.Parallel()
		const secret = "uniqueLeakSentinel-9f3"
		err := errors.New("upstream: token=" + secret)
		got := redaction.RedactError(err)
		if strings.Contains(got.Error(), secret) {
			t.Errorf("redacted error still leaks secret: %q", got.Error())
		}
	})
}

func assertSameErrorIdentity(t *testing.T, got, want error, msg string) {
	t.Helper()
	if !sameErrorIdentity(got, want) {
		t.Fatalf("want exact error identity: got %T %v, want %T %v: %s", got, got, want, want, msg)
	}
}

func assertDifferentErrorIdentity(t *testing.T, got, want error, msg string) {
	t.Helper()
	if sameErrorIdentity(got, want) {
		t.Fatalf("want different error identity: got %T %v, want %T %v: %s", got, got, want, want, msg)
	}
}

func sameErrorIdentity(got, want error) bool {
	if got == nil || want == nil {
		return got == nil && want == nil
	}
	gv, wv := reflect.ValueOf(got), reflect.ValueOf(want)
	if gv.Type() != wv.Type() || !gv.Comparable() {
		return false
	}
	return gv.Equal(wv)
}

func TestTruncateString(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		in     string
		maxLen int
		want   string
	}{
		{name: "short_passthrough", in: "hello", maxLen: 100, want: "hello"},
		{name: "long_truncate", in: "hello world", maxLen: 5, want: "hello"},
		{name: "exact_passthrough", in: "hello", maxLen: 5, want: "hello"},
		{name: "unicode_runeBoundary", in: "你好世界", maxLen: 3, want: "你好世"},
		{name: "zeroMax_passthrough", in: "hello", maxLen: 0, want: "hello"},
		{name: "negativeMax_passthrough", in: "hello", maxLen: -1, want: "hello"},
		{name: "empty", in: "", maxLen: 5, want: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := redaction.TruncateString(tc.in, tc.maxLen)
			if got != tc.want {
				t.Errorf("TruncateString(%q, %d):\n  got:  %q\n  want: %q", tc.in, tc.maxLen, got, tc.want)
			}
		})
	}
}

func TestRedactPanic(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		v    any
		want string
	}{
		{"nil panic value", nil, "<nil>"},
		{
			name: "string panic with secret key=value",
			v:    "config error: password=hunter2 host=db",
			want: "config error: password=" + redaction.Mask + " host=db",
		},
		{
			name: "error panic with token",
			v:    errors.New("dial: token=abc dsn=postgres://u:p@h/db"),
			want: "dial: token=" + redaction.Mask + " dsn=" + redaction.Mask,
		},
		{
			name: "clean panic message passes through unchanged",
			v:    "invariant violated: nil receiver",
			want: "invariant violated: nil receiver",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := redaction.RedactPanic(c.v)
			if got != c.want {
				t.Errorf("RedactPanic(%v) = %q, want %q", c.v, got, c.want)
			}
		})
	}
}

func TestMask_ConstantValue(t *testing.T) {
	t.Parallel()
	if redaction.Mask != "<REDACTED>" {
		t.Errorf("Mask = %q, want %q", redaction.Mask, "<REDACTED>")
	}
}

// TestIsSensitiveKey locks the single-source key matcher used by structured
// scrubbers (OTel safeStringAttr, RedactSlogAttr). Each sensitiveKeyPattern
// keyword + its common camelCase / underscore / separator variants must
// match; non-sensitive names must not.
func TestIsSensitiveKey(t *testing.T) {
	t.Parallel()
	cases := []struct {
		key  string
		want bool
	}{
		// canonical keywords
		{"password", true},
		{"passwd", true},
		{"pwd", true},
		{"secret", true},
		{"token", true},
		{"access_token", true},
		{"access-token", true},
		{"accesstoken", true},
		{"refresh_token", true},
		{"id_token", true},
		{"authorization", true},
		{"bearer", true},
		{"api_key", true},
		{"api-key", true},
		{"apikey", true},
		{"apiKey", true}, // camelCase via optional separator + case-insensitivity
		{"connection_string", true},
		{"connection string", true},
		{"connectionstring", true},
		{"private_key", true},
		{"signing_key", true},
		{"dsn", true},
		// webhook signing surface (KERNEL-WEBHOOK-01)
		{"webhook_secret", true},
		{"webhook-secret", true},
		{"webhooksecret", true},
		{"webhookSecret", true},
		{"x_signature", true},
		{"x-signature", true},
		{"x_hub_signature", true},
		{"x-hub-signature", true},
		{"x_webhook_signature", true},
		{"x-webhook-signature", true},
		{"svix_signature", true},
		{"svix-signature", true},
		{"hmac_key", true},
		{"hmac-key", true},
		// stripe + x-hub-signature-256/-1 variants (SEC-02/SEC-03)
		{"stripe_signature", true},
		{"stripe-signature", true},
		{"x_hub_signature_256", true},
		{"x-hub-signature-256", true},
		{"x-hub-signature-1", true},
		// session identifier — snake / dash / camel / lower (issue #1229)
		{"session_id", true},
		{"session-id", true},
		{"sessionId", true},
		{"sessionid", true},
		// case insensitivity
		{"PASSWORD", true},
		{"API_KEY", true},
		{"Authorization", true},
		// dotted namespace keys — sensitive leaf segment makes whole key sensitive
		{"gocell.principal.session_id", true},
		{"db.password", true},
		{"otel.connection_string", true},
		// dotted namespace keys with non-sensitive segments stay false
		{"gocell.principal.actor_id", false},
		{"gocell.principal.subject_id", false},
		{"gocell.principal.tenant_id", false},
		{"gocell.event.occurred_at_unix_nano", false},
		// non-sensitive
		{"orderID", false},
		{"cell.id", false},
		{"retryCount", false},
		{"config", false},
		{"", false},
		{"username", false},
		// partial match must NOT trigger (exact match only)
		{"my_password_field", false},
		{"password_hint", false},
		{"x-token-prefix", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.key, func(t *testing.T) {
			t.Parallel()
			if got := redaction.IsSensitiveKey(tc.key); got != tc.want {
				t.Errorf("IsSensitiveKey(%q) = %v, want %v", tc.key, got, tc.want)
			}
		})
	}
}

func TestRedactAny(t *testing.T) {
	t.Parallel()

	t.Run("nil_in_nil_out", assertRedactAnyNil)
	t.Run("error_branch_redacts", assertRedactAnyErrorRedacts)
	t.Run("string_branch_redacts", assertRedactAnyStringRedacts)
	t.Run("panic_value_struct_stringified_redacts", assertRedactAnyStructStringifiedRedacts)
	t.Run("int_passthrough_no_secret", assertRedactAnyIntPassthrough)
}

func assertRedactAnyNil(t *testing.T) {
	t.Parallel()
	got := redaction.RedactAny(nil)
	if got != nil {
		t.Errorf("RedactAny(nil) = %v, want nil", got)
	}
}

func assertRedactAnyErrorRedacts(t *testing.T) {
	t.Parallel()
	err := errors.New("password=hunter2")
	got := redaction.RedactAny(err)
	gotErr, ok := got.(error)
	if !ok {
		t.Fatalf("RedactAny(error) returned %T, want error", got)
	}
	if gotErr.Error() != "password=<REDACTED>" {
		t.Errorf("RedactAny(error).Error() = %q, want %q", gotErr.Error(), "password=<REDACTED>")
	}
}

func assertRedactAnyStringRedacts(t *testing.T) {
	t.Parallel()
	got := redaction.RedactAny("token=abc.def")
	gotStr, ok := got.(string)
	if !ok {
		t.Fatalf("RedactAny(string) returned %T, want string", got)
	}
	if gotStr != "token=<REDACTED>" {
		t.Errorf("RedactAny(string) = %q, want %q", gotStr, "token=<REDACTED>")
	}
}

func assertRedactAnyStructStringifiedRedacts(t *testing.T) {
	t.Parallel()
	type panicVal struct{ msg string }
	v := panicVal{msg: "secret=hunter2"}
	got := redaction.RedactAny(v)
	s := fmt.Sprint(got)
	if strings.Contains(s, "hunter2") {
		t.Errorf("RedactAny(struct) still contains sensitive value in %q", s)
	}
	if !strings.Contains(s, "<REDACTED>") {
		t.Errorf("RedactAny(struct) = %q, want it to contain <REDACTED>", s)
	}
}

func assertRedactAnyIntPassthrough(t *testing.T) {
	t.Parallel()
	got := redaction.RedactAny(42)
	s := fmt.Sprint(got)
	if strings.Contains(s, "<REDACTED>") {
		t.Errorf("RedactAny(42) = %q, unexpectedly contains <REDACTED>", s)
	}
	if s != "42" {
		t.Errorf("RedactAny(42) = %q, want %q", s, "42")
	}
}

func TestRedactSlogAttr(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   slog.Attr
		want slog.Attr
	}{
		{
			name: "string_value_with_dsn",
			in:   slog.String("connection", "dsn=postgres://u:p@h:5432/db"),
			want: slog.String("connection", "dsn=<REDACTED>"),
		},
		{
			name: "string_value_without_secret",
			in:   slog.String("orderID", "abc-123"),
			want: slog.String("orderID", "abc-123"),
		},
		{
			name: "int_value_passes_through",
			in:   slog.Int("retryCount", 3),
			want: slog.Int("retryCount", 3),
		},
		{
			// Sensitive key with bare value: IsSensitiveKey(attr.Key) fires
			// first, so the value is masked regardless of whether RedactString
			// would have matched. Pre-fix this returned "secret123" verbatim
			// because the value carries no `password=` anchor — the structured
			// (key, value) leak this two-layer scrubber is designed to close.
			name: "sensitive_key_bare_value_masked",
			in:   slog.String("password", "secret123"),
			want: slog.String("password", redaction.Mask),
		},
		{
			name: "sensitive_key_case_insensitive",
			in:   slog.String("API_KEY", "ak_live_xyz"),
			want: slog.String("API_KEY", redaction.Mask),
		},
		{
			name: "sensitive_key_dsn_url_value",
			in:   slog.String("dsn", "postgres://u:secret@db/prod"),
			want: slog.String("dsn", redaction.Mask),
		},
		{
			name: "sensitive_key_int_value_masked_to_string",
			in:   slog.Int("token", 42),
			want: slog.String("token", redaction.Mask),
		},
		{
			name: "sensitive_key_authorization",
			in:   slog.String("Authorization", "Bearer abc.def"),
			want: slog.String("Authorization", redaction.Mask),
		},
		{
			// Non-sensitive key keeps prior recursive scrub behavior — string
			// value scanned by RedactString, embedded `key=value` masked.
			name: "non_sensitive_key_value_scan_preserved",
			in:   slog.String("orderID", "abc-123 password=leaked"),
			want: slog.String("orderID", "abc-123 password=<REDACTED>"),
		},
		{
			// defaultPattern stops at whitespace (\S+), so "port=5432" survives.
			name: "embedded_password_in_value",
			in:   slog.String("config", "host=h password=secret123 port=5432"),
			want: slog.String("config", "host=h password=<REDACTED> port=5432"),
		},
		{
			// Inner attr key "token" is sensitive — key-aware branch fires on
			// recursion, value collapses to Mask regardless of contents.
			name: "group_value_recurses_sensitive_inner_key",
			in:   slog.Group("ctx", slog.String("token", "bearer=abc.def.ghi")),
			want: slog.Group("ctx", slog.String("token", redaction.Mask)),
		},
		{
			// Group recursion with non-sensitive inner key keeps free-form
			// value scrubbing (bearer= → Mask, whitespace boundary).
			name: "group_value_recurses_nonsensitive_inner_key",
			in:   slog.Group("ctx", slog.String("auth_header", "bearer=abc.def.ghi")),
			want: slog.Group("ctx", slog.String("auth_header", "bearer=<REDACTED>")),
		},
		{
			// Bool value has no string text; passes through unchanged.
			name: "bool_value_passes_through",
			in:   slog.Bool("ok", true),
			want: slog.Bool("ok", true),
		},
		{
			// Nested group recursion: innermost key "apiKey" matches sensitive
			// pattern `api[_-]?key` (case-insensitive, optional separator) →
			// key-aware branch fires after two levels of recursion.
			name: "nested_group_recurses_sensitive_innermost",
			in: slog.Group("outer",
				slog.Group("inner", slog.String("apiKey", "api_key=topsecret"))),
			want: slog.Group("outer",
				slog.Group("inner", slog.String("apiKey", redaction.Mask))),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := redaction.RedactSlogAttr(tc.in)
			if got.Key != tc.want.Key {
				t.Errorf("RedactSlogAttr(%v).Key = %q, want %q", tc.in, got.Key, tc.want.Key)
			}
			if got.Value.String() != tc.want.Value.String() {
				t.Errorf("RedactSlogAttr(%v).Value = %q, want %q",
					tc.in, got.Value.String(), tc.want.Value.String())
			}
		})
	}
}

// customLogValuer implements slog.LogValuer for passthrough boundary testing.
// It returns a fixed string value so the test is deterministic.
type customLogValuer struct{}

func (customLogValuer) LogValue() slog.Value {
	return slog.StringValue("logvaluer-resolved-value")
}

// secretLeakingLogValuer documents the fail-open boundary cost: if a
// LogValuer's resolved string contains a sensitive key=value pattern,
// RedactSlogAttr does NOT recurse into LogValue() and the secret leaks
// to slog. This is by design — the first-line defense is the sealed
// PublicDetail / InternalDetail newtype in pkg/errcode/details.go,
// which routes string values through AsSlogAttr() as slog.KindString
// (covered by RedactString); LogValuer / custom Stringer carriers are
// caller-controlled and must not embed sensitive content.
type secretLeakingLogValuer struct{}

func (secretLeakingLogValuer) LogValue() slog.Value {
	return slog.StringValue("password=hunter2")
}

// TestRedactSlogAttr_PassthroughKinds locks the baseline behavior of
// redactSlogValue for non-string slog.Value kinds: bool, int64, float64,
// time.Time, slog.Any (struct), and slog.LogValuer all pass through unchanged.
//
// This is intentional fail-open design: the regex pipeline only matches
// `key=value` text shapes, so numeric/temporal/structured values cannot
// carry the patterns. Runtime data entering errcode.Error must go through
// the sealed PublicDetail / InternalDetail newtypes (errcode.PublicAttr /
// InternalAttr), which return slog.String (KindString) for string values
// via AsSlogAttr — the first line of defense covering the common
// "embedded key=value inside a string" leak shape.
//
// If a new direct-write path for slog.Any(callerSuppliedStruct) is added,
// extend redactSlogValue with ValueResolve and add cases here.
func TestRedactSlogAttr_PassthroughKinds(t *testing.T) {
	t.Parallel()

	fixedTime := time.Unix(1700000000, 0)

	cases := []struct {
		name           string
		attr           slog.Attr
		wantValueEqual bool // true = value unchanged (passthrough); false = value changed (redacted)
	}{
		{
			name:           "bool passthrough",
			attr:           slog.Bool("flag", true),
			wantValueEqual: true,
		},
		{
			name:           "int64 passthrough",
			attr:           slog.Int64("count", 42),
			wantValueEqual: true,
		},
		{
			name:           "float64 passthrough",
			attr:           slog.Float64("ratio", 3.14),
			wantValueEqual: true,
		},
		{
			name:           "time passthrough",
			attr:           slog.Time("ts", fixedTime),
			wantValueEqual: true,
		},
		{
			name:           "any struct passthrough",
			attr:           slog.Any("obj", struct{ X int }{X: 1}),
			wantValueEqual: true,
		},
		{
			name:           "logvaluer passthrough",
			attr:           slog.Any("v", customLogValuer{}),
			wantValueEqual: true,
		},
		{
			// Documents fail-open boundary cost: LogValuer resolving to a
			// sensitive string IS NOT redacted (passthrough). Acceptable
			// because sealed PublicDetail / InternalDetail upstream route
			// string values through AsSlogAttr as KindString; LogValuer /
			// custom Stringer carriers are out of scope for redaction.
			name:           "logvaluer with secret leaks by design (fail-open boundary)",
			attr:           slog.Any("config", secretLeakingLogValuer{}),
			wantValueEqual: true,
		},
		{
			// Control case: string with sensitive key=value IS redacted.
			name:           "string redacted (control)",
			attr:           slog.String("msg", "password=secret"),
			wantValueEqual: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			original := tc.attr.Value.String()
			got := redaction.RedactSlogAttr(tc.attr)

			if got.Key != tc.attr.Key {
				t.Errorf("RedactSlogAttr key changed: got %q, want %q", got.Key, tc.attr.Key)
			}

			resultEqual := got.Value.String() == original
			if resultEqual != tc.wantValueEqual {
				if tc.wantValueEqual {
					t.Errorf(
						"RedactSlogAttr(%v): expected passthrough (value unchanged), "+
							"but got %q (original %q). KindLogValuer/KindAny should not be "+
							"recursively scanned — fail-open design per Known limitations.",
						tc.attr, got.Value.String(), original,
					)
				} else {
					t.Errorf(
						"RedactSlogAttr(%v): expected redaction (value changed), "+
							"but value unchanged %q. String control case must trigger regex.",
						tc.attr, got.Value.String(),
					)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// F-CR-3: RedactPayload recursive + fail-closed tests
// ---------------------------------------------------------------------------

func TestRedactPayload_NestedObject(t *testing.T) {
	in := []byte(`{"user":{"password":"secret","name":"alice"}}`)
	out := redaction.RedactPayload(in)

	// password inside nested object must be masked.
	if strings.Contains(string(out), "secret") {
		t.Errorf("nested password leaked in output: %s", out)
	}
	// name must survive.
	if !strings.Contains(string(out), "alice") {
		t.Errorf("non-sensitive field 'name' was incorrectly masked: %s", out)
	}
	// Output must start with valid JSON object character.
	if len(out) == 0 || out[0] != '{' {
		t.Errorf("RedactPayload nested object must return JSON object: got %s", out)
	}
}

func TestRedactPayload_NestedArray(t *testing.T) {
	in := []byte(`{"items":[{"token":"abc"},{"token":"xyz"}]}`)
	out := redaction.RedactPayload(in)

	if strings.Contains(string(out), "abc") || strings.Contains(string(out), "xyz") {
		t.Errorf("token inside array was not masked: %s", out)
	}
	if len(out) == 0 || out[0] != '{' {
		t.Errorf("output must be JSON object: got %s", out)
	}
}

func TestRedactPayload_NonJSON_FailClosed(t *testing.T) {
	out := redaction.RedactPayload([]byte("not-json"))
	// Must be a valid JSON string token: starts with '"', ends with '"'.
	s := string(out)
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		t.Errorf("fail-closed output must be a JSON string token, got: %s", s)
	}
	// The unquoted value must be the mask.
	inner := s[1 : len(s)-1]
	if inner != redaction.Mask {
		t.Errorf("fail-closed inner value: got %q, want %q", inner, redaction.Mask)
	}
}

func TestRedactPayload_TopLevelArray(t *testing.T) {
	in := []byte(`[{"password":"x"},{"safe":"y"}]`)
	out := redaction.RedactPayload(in)

	if strings.Contains(string(out), `"x"`) {
		t.Errorf("top-level array password was not masked: %s", out)
	}
	if !strings.Contains(string(out), "y") {
		t.Errorf("non-sensitive field 'safe' was incorrectly masked: %s", out)
	}
	if len(out) == 0 || out[0] != '[' {
		t.Errorf("top-level array output must be JSON array: got %s", out)
	}
}

func TestRedactPayload_Scalar_Unchanged(t *testing.T) {
	// A scalar JSON value (e.g. a number) has no key structure — it is valid
	// JSON and carries no sensitive keys; it must be returned unchanged.
	in := []byte(`42`)
	out := redaction.RedactPayload(in)
	if string(out) != "42" {
		t.Errorf("scalar JSON must pass through unchanged: got %s", out)
	}
}
