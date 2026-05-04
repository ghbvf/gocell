package redaction_test

import (
	"errors"
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
			name: "token_keyEqValue",
			in:   "upstream 401: token=eyJhbGc.foo",
			want: "upstream 401: token=<REDACTED>",
		},
		{
			name: "authorization_colonSpace",
			in:   "Authorization: Bearer abc.def.ghi",
			want: "Authorization: <REDACTED>",
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
		{ //nolint:gosec // G101 false positive: synthetic DSN fixture demonstrates redaction of scheme://user:pwd@ form
			name: "dsn_postgres",
			in:   "connect failed: dsn=postgres://USR:PWD@h/db trailing",
			want: "connect failed: dsn=<REDACTED> trailing",
		},
		{
			name: "connection_string_underscore",
			in:   "connection_string=Server=foo;Pwd=bar",
			want: "connection_string=<REDACTED>",
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
		if got != err {
			t.Errorf("RedactError(plain) returned different instance; identity must be preserved when nothing changes")
		}
	})

	t.Run("redacted_returnsNewInstance", func(t *testing.T) {
		t.Parallel()
		original := errors.New("password=hunter2")
		got := redaction.RedactError(original)
		if got == original {
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

func TestMask_ConstantValue(t *testing.T) {
	t.Parallel()
	if redaction.Mask != "<REDACTED>" {
		t.Errorf("Mask = %q, want %q", redaction.Mask, "<REDACTED>")
	}
}
