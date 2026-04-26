package httputil_test

import (
	"strings"
	"testing"

	"github.com/ghbvf/gocell/pkg/httputil"
)

func TestParseCanonicalUUID(t *testing.T) {
	t.Parallel()

	const canonical = "0e8d6e9a-3a6f-4b1f-9c1e-2a3b4c5d6e7f"
	const compact = "0e8d6e9a3a6f4b1f9c1e2a3b4c5d6e7f"

	tests := []struct {
		name string
		raw  string
		want string
		ok   bool
	}{
		{name: "canonical lowercase", raw: canonical, want: canonical, ok: true},
		{name: "canonical uppercase normalized", raw: strings.ToUpper(canonical), want: canonical, ok: true},
		{name: "compact lowercase", raw: compact, want: canonical, ok: true},
		{name: "compact uppercase", raw: strings.ToUpper(compact), want: canonical, ok: true},

		// google/uuid.Parse accepts these — ParseCanonicalUUID rejects.
		{name: "brace wrapped rejected", raw: "{" + canonical + "}"},
		{name: "urn prefix rejected", raw: "urn:uuid:" + canonical},
		{
			// Length 38 happens to match the brace-dispatch branch in google/uuid
			// when both ends carry whitespace; only this exact shape sneaks past
			// length-only checks, which is why the helper relies on length 32/36.
			name: "whitespace padded both ends rejected (length-38 collision)",
			raw:  " " + canonical + " ",
		},
		{name: "leading space rejected", raw: " " + canonical},
		{name: "trailing space rejected", raw: canonical + " "},
		{name: "leading tab rejected", raw: "\t" + canonical},
		{name: "embedded whitespace rejected", raw: canonical[:8] + " " + canonical[9:]},

		// Length-only failure modes.
		{name: "empty rejected", raw: ""},
		{name: "too short rejected", raw: "abc"},
		{name: "too long rejected", raw: canonical + "extra"},
		{name: "nondashed but wrong hex rejected", raw: strings.Repeat("g", 32)},
		{name: "dashed but malformed rejected", raw: strings.Repeat("z", 36)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := httputil.ParseCanonicalUUID(tt.raw)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v (got=%q)", ok, tt.ok, got)
			}
			if got != tt.want {
				t.Fatalf("value = %q, want %q", got, tt.want)
			}
		})
	}
}
