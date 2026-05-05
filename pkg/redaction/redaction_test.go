package redaction_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

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
		if !errors.Is(got, err) {
			t.Errorf("RedactError(plain) returned different instance; identity must be preserved when nothing changes")
		}
	})

	t.Run("redacted_returnsNewInstance", func(t *testing.T) {
		t.Parallel()
		original := errors.New("password=hunter2")
		got := redaction.RedactError(original)
		if errors.Is(got, original) {
			t.Fatal("RedactError(sensitive) returned same instance; expected a new error with masked text")
		}
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
