package spiffeid_test

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

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
				require.Error(t, err, "ForCell(%q,%q) = %v, want error", tc.trustDomain, tc.cell, got)
				return
			}
			require.NoError(t, err, "ForCell(%q,%q)", tc.trustDomain, tc.cell)
			require.Equal(t, tc.wantString, got.String())
			require.Equal(t, tc.trustDomain, got.TrustDomain())
			require.Equal(t, tc.cell, got.Cell())
			require.False(t, got.IsZero(), "IsZero() = true for a constructed ID")
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
		// Non-canonical URL components on a cell-shaped URI must fail closed and
		// must NOT be normalized to the clean ID they resemble (#2297 F1).
		{name: "userinfo rejected", raw: "spiffe://evil@example.org/cell/accesscore", wantErr: true},
		{name: "query rejected", raw: "spiffe://example.org/cell/accesscore?x=1", wantErr: true},
		{name: "fragment rejected", raw: "spiffe://example.org/cell/accesscore#frag", wantErr: true},
		{name: "port rejected", raw: "spiffe://example.org:8443/cell/accesscore", wantErr: true},
		{name: "userinfo+query+fragment combined rejected", raw: "spiffe://evil@example.org/cell/accesscore?x#y", wantErr: true},
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

// TestCellSetFromURIs covers the #2297 allow-set extractor: a certificate's URI
// SANs yield the FULL set of distinct cell SPIFFE ids (a multi-cell workload
// cert), all sharing one trust domain. A cert bridging two trust domains is
// rejected; non-cell spiffe URIs are ignored; duplicates collapse.
func TestCellSetFromURIs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		uris      []*url.URL
		wantErr   bool
		wantTD    string
		wantCells []string // expected members
	}{
		{name: "nil -> empty set", uris: nil},
		{name: "no spiffe -> empty set", uris: []*url.URL{mustURL(t, "https://example.org/foo")}},
		{
			name:      "single cell id",
			uris:      []*url.URL{mustURL(t, "spiffe://example.org/cell/accesscore")},
			wantTD:    "example.org",
			wantCells: []string{"accesscore"},
		},
		{
			name: "multiple distinct cell ids (same trust domain) -> full set",
			uris: []*url.URL{
				mustURL(t, "spiffe://example.org/cell/accesscore"),
				mustURL(t, "spiffe://example.org/cell/configcore"),
			},
			wantTD:    "example.org",
			wantCells: []string{"accesscore", "configcore"},
		},
		{
			name: "cell id alongside non-cell spiffe (ignored)",
			uris: []*url.URL{
				mustURL(t, "spiffe://example.org/ns/edge/sa/wl-1"),
				mustURL(t, "spiffe://example.org/cell/configcore"),
			},
			wantTD:    "example.org",
			wantCells: []string{"configcore"},
		},
		{
			name: "duplicate identical cell ids collapse",
			uris: []*url.URL{
				mustURL(t, "spiffe://example.org/cell/accesscore"),
				mustURL(t, "spiffe://example.org/cell/accesscore"),
			},
			wantTD:    "example.org",
			wantCells: []string{"accesscore"},
		},
		{
			name: "mixed trust domains -> error (a cert must not bridge trust domains)",
			uris: []*url.URL{
				mustURL(t, "spiffe://example.org/cell/accesscore"),
				mustURL(t, "spiffe://other.org/cell/configcore"),
			},
			wantErr: true,
		},
		// A cell-shaped SPIFFE URI with non-canonical components must fail closed,
		// not be silently dropped or normalized to a member (#2297 F1).
		{
			name:    "cell-shaped with userinfo -> error",
			uris:    []*url.URL{mustURL(t, "spiffe://evil@example.org/cell/accesscore")},
			wantErr: true,
		},
		{
			name:    "cell-shaped with query -> error",
			uris:    []*url.URL{mustURL(t, "spiffe://example.org/cell/accesscore?x=1")},
			wantErr: true,
		},
		{
			name:    "cell-shaped with fragment -> error",
			uris:    []*url.URL{mustURL(t, "spiffe://example.org/cell/accesscore#frag")},
			wantErr: true,
		},
		{
			name:    "cell-shaped with port -> error",
			uris:    []*url.URL{mustURL(t, "spiffe://example.org:8443/cell/accesscore")},
			wantErr: true,
		},
		{
			// Fail-closed: a forged non-canonical ID presented ALONGSIDE a clean
			// member must reject the whole cert, not normalize evil@ to the member.
			name: "clean member + forged userinfo id -> error (whole cert rejected)",
			uris: []*url.URL{
				mustURL(t, "spiffe://example.org/cell/accesscore"),
				mustURL(t, "spiffe://evil@example.org/cell/accesscore?x#y"),
			},
			wantErr: true,
		},
		{
			// Boundary: non-cell-shaped spiffe SANs are foreign and stay IGNORED
			// even when they carry components — only cell-shaped URIs are gated.
			name: "non-cell spiffe with query ignored (only cell-shaped is gated)",
			uris: []*url.URL{
				mustURL(t, "spiffe://example.org/ns/edge/sa/wl-1?x=1"),
				mustURL(t, "spiffe://example.org/cell/configcore"),
			},
			wantTD:    "example.org",
			wantCells: []string{"configcore"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			set, err := spiffeid.CellSetFromURIs(tc.uris)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, len(tc.wantCells), set.Len(), "set cardinality")
			if len(tc.wantCells) == 0 {
				require.True(t, set.IsEmpty())
				return
			}
			require.False(t, set.IsEmpty())
			require.Equal(t, tc.wantTD, set.TrustDomain())
			for _, c := range tc.wantCells {
				id, err := spiffeid.ForCell(tc.wantTD, c)
				require.NoError(t, err)
				require.True(t, set.Contains(id), "set must contain %s", id.String())
			}
		})
	}
}

// TestCellSetString exercises CellSet.String(): 空集返回 "[]"；单元素返回带完整 URI
// 的括号表示；两元素无论传入顺序如何，输出都按字母序排定（排序稳定性）。
func TestCellSetString(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		uris    []*url.URL
		wantStr string
	}{
		{
			name:    "empty set -> []",
			uris:    nil,
			wantStr: "[]",
		},
		{
			name:    "single element",
			uris:    []*url.URL{mustURL(t, "spiffe://example.org/cell/accesscore")},
			wantStr: "[spiffe://example.org/cell/accesscore]",
		},
		{
			// 两元素：无论传入顺序，输出都按 configcore < accesscore 的字母序排列。
			name: "two elements sorted (input order: configcore first)",
			uris: []*url.URL{
				mustURL(t, "spiffe://example.org/cell/configcore"),
				mustURL(t, "spiffe://example.org/cell/accesscore"),
			},
			wantStr: "[spiffe://example.org/cell/accesscore spiffe://example.org/cell/configcore]",
		},
		{
			// 相同两元素，换传入顺序，输出不变——验证排序稳定。
			name: "two elements sorted (input order: accesscore first)",
			uris: []*url.URL{
				mustURL(t, "spiffe://example.org/cell/accesscore"),
				mustURL(t, "spiffe://example.org/cell/configcore"),
			},
			wantStr: "[spiffe://example.org/cell/accesscore spiffe://example.org/cell/configcore]",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			set, err := spiffeid.CellSetFromURIs(tc.uris)
			require.NoError(t, err)
			require.Equal(t, tc.wantStr, set.String())
		})
	}
}

// TestCellSetCells exercises CellSet.Cells(): 空集返回 nil；非空集返回排序后的 []CellID。
func TestCellSetCells(t *testing.T) {
	t.Parallel()

	t.Run("empty set returns nil", func(t *testing.T) {
		t.Parallel()
		var empty spiffeid.CellSet
		require.Nil(t, empty.Cells())
	})

	t.Run("single element", func(t *testing.T) {
		t.Parallel()
		set, err := spiffeid.CellSetFromURIs([]*url.URL{
			mustURL(t, "spiffe://example.org/cell/accesscore"),
		})
		require.NoError(t, err)
		cells := set.Cells()
		require.Len(t, cells, 1)
		require.Equal(t, "spiffe://example.org/cell/accesscore", cells[0].String())
	})

	t.Run("two elements are sorted", func(t *testing.T) {
		t.Parallel()
		// 传入顺序 configcore, accesscore；期望 Cells() 返回 accesscore, configcore（字母序）。
		set, err := spiffeid.CellSetFromURIs([]*url.URL{
			mustURL(t, "spiffe://example.org/cell/configcore"),
			mustURL(t, "spiffe://example.org/cell/accesscore"),
		})
		require.NoError(t, err)
		cells := set.Cells()
		require.Len(t, cells, 2)
		require.Equal(t, "spiffe://example.org/cell/accesscore", cells[0].String())
		require.Equal(t, "spiffe://example.org/cell/configcore", cells[1].String())
	})
}

// TestCellSetContains exercises the sole membership predicate (the go-spiffe
// AuthorizeMemberOf analog): trust domain + cell must both match, the zero CellID
// is never a member, and the empty set contains nothing.
func TestCellSetContains(t *testing.T) {
	t.Parallel()
	set, err := spiffeid.CellSetFromURIs([]*url.URL{
		mustURL(t, "spiffe://example.org/cell/accesscore"),
		mustURL(t, "spiffe://example.org/cell/configcore"),
	})
	require.NoError(t, err)

	member, _ := spiffeid.ForCell("example.org", "accesscore")
	require.True(t, set.Contains(member), "a member cell must be Contains-true")

	nonMember, _ := spiffeid.ForCell("example.org", "auditcore")
	require.False(t, set.Contains(nonMember), "a non-member cell must be Contains-false")

	wrongTD, _ := spiffeid.ForCell("other.org", "accesscore")
	require.False(t, set.Contains(wrongTD), "same cell name in a different trust domain must not match")

	require.False(t, set.Contains(spiffeid.CellID{}), "zero CellID must never be a member")

	var empty spiffeid.CellSet
	require.True(t, empty.IsEmpty())
	require.Equal(t, 0, empty.Len())
	require.Equal(t, "", empty.TrustDomain())
	require.False(t, empty.Contains(member), "the empty set contains nothing")
}
