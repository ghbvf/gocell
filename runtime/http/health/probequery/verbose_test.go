package probequery_test

import (
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/runtime/http/health/probequery"
)

func TestVerbose(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"absent", "", false},
		{"bare_key", "verbose", true},
		{"empty_value", "verbose=", true},
		{"one", "verbose=1", true},
		{"true_lower", "verbose=true", true},
		{"true_upper", "verbose=TRUE", true},
		{"true_mixed_with_spaces", "verbose=%20%20True%20%20", true},
		{"zero", "verbose=0", false},
		{"false", "verbose=false", false},
		{"yes", "verbose=yes", false},
		{"debug", "verbose=debug", false},
		{"empty_and_zero_returns_true", "verbose=&verbose=0", true},
		{"zero_and_empty_returns_true", "verbose=0&verbose=", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := "/readyz"
			if tc.raw != "" {
				path = "/readyz?" + tc.raw
			}
			r := httptest.NewRequest("GET", path, nil)
			if got := probequery.Verbose(r); got != tc.want {
				t.Fatalf("Verbose(%q) = %v, want %v", path, got, tc.want)
			}
		})
	}
}

func TestVerbose_OtherQueryParamsIgnored(t *testing.T) {
	t.Parallel()
	r := httptest.NewRequest("GET", "/readyz?other=true&also=1", nil)
	if probequery.Verbose(r) {
		t.Fatal("Verbose must be false when ?verbose is absent regardless of other params")
	}
}
