package otel

import (
	"strings"
	"testing"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"

	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/redaction"
)

// TestSafeStringAttr_RedactsSensitiveSubstrings asserts SPAN-SETATTR-REDACT-01:
// every string-valued span attribute is routed through pkg/redaction.RedactString
// regardless of caller-supplied content. Mask key list is the single source in
// pkg/redaction (sensitiveKeyPattern).
func TestSafeStringAttr_RedactsSensitiveSubstrings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		key  string
		raw  string
		want string // substring that MUST appear in the emitted attribute value
		// substringNot lists strings whose presence in the output indicates the
		// redactor failed to mask the secret value. Nil means "no negative
		// assertion" (e.g. for the all-plain case below).
		substringNot []string
	}{
		{
			name:         "plain text passes through unchanged",
			key:          "cell.id",
			raw:          "accesscore",
			want:         "accesscore",
			substringNot: nil,
		},
		{
			name:         "password=value gets masked",
			key:          "http.error",
			raw:          "auth failed: password=hunter2 user=alice",
			want:         redaction.Mask,
			substringNot: []string{"hunter2"},
		},
		{
			name:         "Authorization: Bearer ... gets masked",
			key:          "http.header",
			raw:          "Authorization: Bearer abc.def.ghi",
			want:         redaction.Mask,
			substringNot: []string{"abc.def.ghi"},
		},
		{
			name:         "token JSON form gets masked",
			key:          "payload",
			raw:          `{"token":"secret-jwt-payload"}`,
			want:         redaction.Mask,
			substringNot: []string{"secret-jwt-payload"},
		},
		{
			name:         "dsn key value gets masked",
			key:          "db.url",
			raw:          "connecting: dsn=opaque-credential-payload",
			want:         redaction.Mask,
			substringNot: []string{"opaque-credential-payload"},
		},
		{
			name:         "api_key=... gets masked",
			key:          "service.creds",
			raw:          "client init: api_key=ak_live_xxx region=us",
			want:         redaction.Mask,
			substringNot: []string{"ak_live_xxx"},
		},
		{
			// Structured key-aware path: bare value with no `password=`
			// anchor — RedactString alone would not match; IsSensitiveKey
			// fires on the key and the value collapses to Mask.
			name:         "sensitive key 'password' + bare value gets masked",
			key:          "password",
			raw:          "hunter2",
			want:         redaction.Mask,
			substringNot: []string{"hunter2"},
		},
		{
			name: "sensitive key 'dsn' + raw url value gets masked",
			key:  "dsn",
			// #nosec G101 -- test fixture; this URL shape is exactly the leak
			// surface the key-aware branch closes.
			raw:          "postgres://u:secret@db/prod",
			want:         redaction.Mask,
			substringNot: []string{"secret", "postgres", "prod"},
		},
		{
			name:         "sensitive key 'API_KEY' (case-insensitive) gets masked",
			key:          "API_KEY",
			raw:          "ak_live_xyz",
			want:         redaction.Mask,
			substringNot: []string{"ak_live_xyz"},
		},
		{
			name:         "sensitive key 'Authorization' + bare bearer gets masked",
			key:          "Authorization",
			raw:          "Bearer abc.def",
			want:         redaction.Mask,
			substringNot: []string{"abc.def"},
		},
		{
			// Negative: non-sensitive key with plain value stays passthrough.
			name:         "non-sensitive key 'cell.id' stays passthrough",
			key:          "cell.id",
			raw:          "accesscore",
			want:         "accesscore",
			substringNot: nil,
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			kv := safeStringAttr(tc.key, tc.raw)
			if string(kv.Key) != tc.key {
				t.Fatalf("key = %q, want %q", string(kv.Key), tc.key)
			}
			if kv.Value.Type() != attribute.STRING {
				t.Fatalf("attr type = %v, want STRING", kv.Value.Type())
			}
			got := kv.Value.AsString()
			if !strings.Contains(got, tc.want) {
				t.Errorf("safeStringAttr(%q, %q) = %q, missing want substring %q",
					tc.key, tc.raw, got, tc.want)
			}
			for _, leak := range tc.substringNot {
				if strings.Contains(got, leak) {
					t.Errorf("safeStringAttr(%q, %q) = %q, MUST NOT contain leaked value %q",
						tc.key, tc.raw, got, leak)
				}
			}
		})
	}
}

// TestSafeStringAttr_TruncatesAtCap asserts that string attribute values are
// rune-truncated to attrValueMaxLen so an attacker cannot pad a span with a
// multi-megabyte attribute (fail-closed denial-of-service surface).
//
// Boundary cases at maxLen-1 / maxLen / maxLen+1 confirm the inclusive-equal
// rule from pkg/redaction.TruncateString and that the truncation is rune-aware
// (not byte-aware), so a multi-byte UTF-8 input near the boundary does not
// produce an invalid UTF-8 tail.
func TestSafeStringAttr_TruncatesAtCap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		runeCount int
		wantRunes int
	}{
		{"empty string", 0, 0},
		{"below cap unchanged", attrValueMaxLen - 1, attrValueMaxLen - 1},
		{"at cap unchanged", attrValueMaxLen, attrValueMaxLen},
		{"above cap truncated", attrValueMaxLen + 1, attrValueMaxLen},
		{"far above cap truncated", attrValueMaxLen * 3, attrValueMaxLen},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			raw := strings.Repeat("a", tc.runeCount)
			kv := safeStringAttr("k", raw)
			got := kv.Value.AsString()
			if utf8.RuneCountInString(got) != tc.wantRunes {
				t.Errorf("rune count = %d, want %d", utf8.RuneCountInString(got), tc.wantRunes)
			}
			if !utf8.ValidString(got) {
				t.Errorf("truncated value contains invalid UTF-8")
			}
		})
	}
}

// TestSafeStringAttr_TruncatesMultiByteUTF8 confirms rune-aware truncation
// applied to a multi-byte CJK input near the boundary. A byte-based truncation
// would slice mid-rune and yield invalid UTF-8.
func TestSafeStringAttr_TruncatesMultiByteUTF8(t *testing.T) {
	t.Parallel()
	raw := strings.Repeat("漢", attrValueMaxLen+5) // each rune is 3 bytes
	kv := safeStringAttr("k", raw)
	got := kv.Value.AsString()
	if utf8.RuneCountInString(got) != attrValueMaxLen {
		t.Errorf("rune count = %d, want %d", utf8.RuneCountInString(got), attrValueMaxLen)
	}
	if !utf8.ValidString(got) {
		t.Errorf("multi-byte truncation produced invalid UTF-8")
	}
}

// TestSafeBytesAttr_PreservesSHA256Format confirms the existing []byte path
// (SHA256 hash + length metadata) is unchanged by the funnel extraction.
// This is the canonical fail-closed shape for binary payloads — RedactString
// is meaningless on binary, and SHA256 retains debugging value (operators can
// correlate by hash without exposing the payload).
func TestSafeBytesAttr_PreservesSHA256Format(t *testing.T) {
	t.Parallel()
	payload := []byte("secret-binary-payload-content")
	kv := safeBytesAttr("body", payload)
	if string(kv.Key) != "body" {
		t.Fatalf("key = %q, want %q", string(kv.Key), "body")
	}
	got := kv.Value.AsString()
	if !strings.HasPrefix(got, "[redacted bytes len=") {
		t.Errorf("safeBytesAttr value = %q, want SHA256 metadata prefix", got)
	}
	if !strings.Contains(got, "sha256=") {
		t.Errorf("safeBytesAttr value = %q, missing sha256= marker", got)
	}
	if strings.Contains(got, "secret-binary-payload-content") {
		t.Errorf("safeBytesAttr leaked raw bytes: %q", got)
	}
}

// TestAttrToKeyValue_DispatchesAllBranches exercises every wrapper.Attr.Value
// type through the dispatch in attrToKeyValue. String + default branches must
// route through safeStringAttr (redaction applied); int / int64 / float64 /
// bool must route through native attribute constructors unchanged; []byte must
// route through safeBytesAttr (SHA256 metadata).
func TestAttrToKeyValue_DispatchesAllBranches(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		attr      wrapper.Attr
		wantType  attribute.Type
		wantValue any      // typed compare per wantType
		notWant   []string // substrings that must NOT appear in the output
	}{
		{
			name:      "string branch redacts",
			attr:      wrapper.Attr{Key: "k", Value: "token=abc"},
			wantType:  attribute.STRING,
			wantValue: "token=" + redaction.Mask,
		},
		{
			name:      "string branch plain pass-through",
			attr:      wrapper.Attr{Key: "k", Value: "plain"},
			wantType:  attribute.STRING,
			wantValue: "plain",
		},
		{
			name:      "int branch",
			attr:      wrapper.Attr{Key: "k", Value: 42},
			wantType:  attribute.INT64,
			wantValue: int64(42),
		},
		{
			name:      "int64 branch",
			attr:      wrapper.Attr{Key: "k", Value: int64(7)},
			wantType:  attribute.INT64,
			wantValue: int64(7),
		},
		{
			name:      "float64 branch",
			attr:      wrapper.Attr{Key: "k", Value: 3.14},
			wantType:  attribute.FLOAT64,
			wantValue: 3.14,
		},
		{
			name:      "bool branch",
			attr:      wrapper.Attr{Key: "k", Value: true},
			wantType:  attribute.BOOL,
			wantValue: true,
		},
		{
			name:      "default branch (struct fmt.Sprint) gets redacted",
			attr:      wrapper.Attr{Key: "k", Value: struct{ S string }{S: "password=xyz"}},
			wantType:  attribute.STRING,
			wantValue: "password=" + redaction.Mask,
			notWant:   []string{"xyz"},
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			kv := attrToKeyValue(tc.attr)
			if kv.Value.Type() != tc.wantType {
				t.Fatalf("type = %v, want %v", kv.Value.Type(), tc.wantType)
			}
			switch want := tc.wantValue.(type) {
			case string:
				got := kv.Value.AsString()
				if !strings.Contains(got, want) {
					t.Errorf("string value = %q, expected to contain %q", got, want)
				}
				for _, leak := range tc.notWant {
					if strings.Contains(got, leak) {
						t.Errorf("default branch leaked raw value: %q contains %q", got, leak)
					}
				}
			case int64:
				if kv.Value.AsInt64() != want {
					t.Errorf("int64 value = %d, want %d", kv.Value.AsInt64(), want)
				}
			case float64:
				if kv.Value.AsFloat64() != want {
					t.Errorf("float64 value = %v, want %v", kv.Value.AsFloat64(), want)
				}
			case bool:
				if kv.Value.AsBool() != want {
					t.Errorf("bool value = %v, want %v", kv.Value.AsBool(), want)
				}
			}
		})
	}
}

// TestAttrToKeyValue_BytesBranchRoutesSHA256 confirms the []byte case stays
// on the SHA256 path (not RedactString); the funnel split must not silently
// reroute binary payloads through string-shape regex masking.
func TestAttrToKeyValue_BytesBranchRoutesSHA256(t *testing.T) {
	t.Parallel()
	kv := attrToKeyValue(wrapper.Attr{Key: "body", Value: []byte("password=plaintext-in-bytes")})
	got := kv.Value.AsString()
	if !strings.HasPrefix(got, "[redacted bytes len=") {
		t.Errorf("bytes branch did not route through SHA256 path: %q", got)
	}
	if strings.Contains(got, "plaintext-in-bytes") {
		t.Errorf("bytes branch leaked raw payload: %q", got)
	}
}

// Ordering invariant (RedactString MUST precede TruncateString in
// safeStringAttr's free-form branch) is enforced by archtest A3b
// (tools/archtest/span_setattr_redact_test.go) via pure-AST form-uniqueness
// on the return expression. Behavioral unit tests on a single input cannot
// discriminate the two orderings — every input that hits a sensitive
// substring either masks visibly under both orders (no leak observable) or
// leaks tail bytes (depends on cap arithmetic). A3b's source-level form
// lock is strictly stronger than any single-call behavioral assertion;
// keeping a redundant unit test would document a guarantee it does not
// provide.
