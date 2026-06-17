package spiffeid_test

import (
	"net/url"
	"testing"

	"github.com/ghbvf/gocell/framework/pkg/spiffeid"
)

func TestForCell(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		trustDomain string
		cell        string
		wantErr     bool
		wantString  string
	}{
		{name: "valid", trustDomain: "example.org", cell: "accesscore", wantString: "spiffe://example.org/cell/accesscore"},
		{
			name: "valid with hyphen+underscore in td", trustDomain: "gocell-prod_1.internal", cell: "configcore",
			wantString: "spiffe://gocell-prod_1.internal/cell/configcore",
		},
		{name: "empty trust domain", trustDomain: "", cell: "accesscore", wantErr: true},
		{name: "empty cell", trustDomain: "example.org", cell: "", wantErr: true},
		{name: "whitespace cell", trustDomain: "example.org", cell: " ", wantErr: true},
		{name: "uppercase trust domain rejected", trustDomain: "Example.org", cell: "accesscore", wantErr: true},
		{name: "trust domain with scheme rejected", trustDomain: "spiffe://example.org", cell: "accesscore", wantErr: true},
		{name: "trust domain with slash rejected", trustDomain: "example.org/x", cell: "accesscore", wantErr: true},
		{name: "cell with slash rejected", trustDomain: "example.org", cell: "access/core", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := spiffeid.ForCell(tc.trustDomain, tc.cell)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ForCell(%q,%q) = %v, want error", tc.trustDomain, tc.cell, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ForCell(%q,%q) unexpected error: %v", tc.trustDomain, tc.cell, err)
			}
			if got.String() != tc.wantString {
				t.Fatalf("String() = %q, want %q", got.String(), tc.wantString)
			}
			if got.TrustDomain() != tc.trustDomain {
				t.Fatalf("TrustDomain() = %q, want %q", got.TrustDomain(), tc.trustDomain)
			}
			if got.Cell() != tc.cell {
				t.Fatalf("Cell() = %q, want %q", got.Cell(), tc.cell)
			}
			if got.IsZero() {
				t.Fatalf("IsZero() = true for a constructed ID")
			}
		})
	}
}

func TestParse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		raw     string
		wantErr bool
		wantTD  string
		wantCel string
	}{
		{name: "valid", raw: "spiffe://example.org/cell/accesscore", wantErr: false, wantTD: "example.org", wantCel: "accesscore"},
		{name: "wrong scheme http", raw: "https://example.org/cell/accesscore", wantErr: true},
		{name: "no scheme", raw: "example.org/cell/accesscore", wantErr: true},
		{name: "missing cell segment", raw: "spiffe://example.org/cell/", wantErr: true},
		{name: "not a cell path", raw: "spiffe://example.org/ns/edge/sa/wl-1", wantErr: true},
		{name: "extra path segments", raw: "spiffe://example.org/cell/accesscore/extra", wantErr: true},
		{name: "empty host", raw: "spiffe:///cell/accesscore", wantErr: true},
		{name: "empty", raw: "", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := spiffeid.Parse(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Parse(%q) = %v, want error", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q) unexpected error: %v", tc.raw, err)
			}
			if got.TrustDomain() != tc.wantTD || got.Cell() != tc.wantCel {
				t.Fatalf("Parse(%q) = (%q,%q), want (%q,%q)", tc.raw, got.TrustDomain(), got.Cell(), tc.wantTD, tc.wantCel)
			}
		})
	}
}

func TestParseRoundTrip(t *testing.T) {
	t.Parallel()
	orig, err := spiffeid.ForCell("example.org", "accesscore")
	if err != nil {
		t.Fatal(err)
	}
	got, err := spiffeid.Parse(orig.String())
	if err != nil {
		t.Fatalf("Parse(%q): %v", orig.String(), err)
	}
	if !got.Equal(orig) {
		t.Fatalf("round-trip Equal = false: %q vs %q", got.String(), orig.String())
	}
}

func TestEqual(t *testing.T) {
	t.Parallel()
	a, _ := spiffeid.ForCell("example.org", "accesscore")
	b, _ := spiffeid.ForCell("example.org", "accesscore")
	c, _ := spiffeid.ForCell("example.org", "configcore")
	d, _ := spiffeid.ForCell("other.org", "accesscore")
	if !a.Equal(b) {
		t.Fatal("same td+cell must be Equal")
	}
	if a.Equal(c) {
		t.Fatal("different cell must not be Equal")
	}
	if a.Equal(d) {
		t.Fatal("different trust domain must not be Equal")
	}
	var zero spiffeid.CellID
	if !zero.IsZero() {
		t.Fatal("zero value IsZero must be true")
	}
	if a.IsZero() {
		t.Fatal("constructed must not be zero")
	}
	// Zero-value boundary: Equal against a zero CellID must return false in both
	// directions (the zero value is invalid and must never compare equal to any
	// constructed ID).
	if zero.Equal(a) {
		t.Fatal("zero.Equal(constructed) must be false")
	}
	if a.Equal(zero) {
		t.Fatal("constructed.Equal(zero) must be false")
	}
}

// TestValidateTrustDomain exercises the exported ValidateTrustDomain helper
// directly — table-driven, covering the empty / valid / uppercase / colon /
// slash cases that gocell-validate and composition-root resolvers depend on.
func TestValidateTrustDomain(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		td      string
		wantErr bool
	}{
		{name: "empty", td: "", wantErr: true},
		{name: "valid lowercase+digits+dots+hyphen+underscore", td: "gocell-prod_1.internal", wantErr: false},
		{name: "valid simple", td: "example.org", wantErr: false},
		{name: "uppercase rejected", td: "Example.ORG", wantErr: true},
		{name: "contains colon rejected", td: "example.org:443", wantErr: true},
		{name: "contains slash rejected", td: "example.org/path", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := spiffeid.ValidateTrustDomain(tc.td)
			if tc.wantErr && err == nil {
				t.Fatalf("ValidateTrustDomain(%q) = nil, want error", tc.td)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateTrustDomain(%q) unexpected error: %v", tc.td, err)
			}
		})
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url %q: %v", raw, err)
	}
	return u
}

func TestFromURIs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		uris    []*url.URL
		wantOK  bool
		wantErr bool
		wantCel string
	}{
		{name: "nil", uris: nil, wantOK: false},
		{name: "no spiffe", uris: []*url.URL{mustURL(t, "https://example.org/foo")}, wantOK: false},
		{
			name:    "one cell id",
			uris:    []*url.URL{mustURL(t, "spiffe://example.org/cell/accesscore")},
			wantOK:  true,
			wantCel: "accesscore",
		},
		{
			name: "cell id alongside non-cell spiffe (ignored)",
			uris: []*url.URL{
				mustURL(t, "spiffe://example.org/ns/edge/sa/wl-1"),
				mustURL(t, "spiffe://example.org/cell/configcore"),
			},
			wantOK:  true,
			wantCel: "configcore",
		},
		{
			name: "two distinct cell ids -> ambiguous error",
			uris: []*url.URL{
				mustURL(t, "spiffe://example.org/cell/accesscore"),
				mustURL(t, "spiffe://example.org/cell/configcore"),
			},
			wantErr: true,
		},
		{
			name: "two identical cell ids -> ok",
			uris: []*url.URL{
				mustURL(t, "spiffe://example.org/cell/accesscore"),
				mustURL(t, "spiffe://example.org/cell/accesscore"),
			},
			wantOK:  true,
			wantCel: "accesscore",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok, err := spiffeid.FromURIs(tc.uris)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("FromURIs = (%v,%v,nil), want error", got, ok)
				}
				return
			}
			if err != nil {
				t.Fatalf("FromURIs unexpected error: %v", err)
			}
			if ok != tc.wantOK {
				t.Fatalf("FromURIs ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && got.Cell() != tc.wantCel {
				t.Fatalf("FromURIs cell = %q, want %q", got.Cell(), tc.wantCel)
			}
		})
	}
}
