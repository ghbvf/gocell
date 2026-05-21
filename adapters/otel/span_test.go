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
		wantValue any // typed compare per wantType
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

// TestAttrToKeyValue_RedactBeforeTruncateOrder is a regression guard for the
// helper's documented redact-then-truncate ordering. If a future edit swaps
// the order, a sensitive substring whose mask form `<key>=<REDACTED>` is
// shorter than the original value would still get correctly masked, but a
// substring whose value happens to span the truncation boundary would leak
// its tail unmasked.
//
// Test shape: build an input where the secret value sits past the cap. If
// truncate runs first, the regex never matches and the (now-truncated)
// `password=` prefix appears in the output without a mask. If redact runs
// first (correct order), the entire `password=<REDACTED>` mask appears
// regardless of the cap, and the truncation then trims the post-mask tail.
func TestAttrToKeyValue_RedactBeforeTruncateOrder(t *testing.T) {
	t.Parallel()
	padding := strings.Repeat("x", attrValueMaxLen-5) // leaves 5 runes of room at the cap boundary
	raw := padding + " password=supersecret"
	kv := attrToKeyValue(wrapper.Attr{Key: "k", Value: raw})
	got := kv.Value.AsString()
	if strings.Contains(got, "supersecret") {
		t.Errorf("redact ran AFTER truncate: secret leaked at boundary. got=%q", got)
	}
}
