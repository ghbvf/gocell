package metadatatest_test

import (
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata/metadatatest"
	"github.com/ghbvf/gocell/pkg/errcode"
)

func TestNewCellID_Valid(t *testing.T) {
	cases := []string{
		"accesscore",
		"sharedcrypto",
		"x9",
		"auditcore",
		"aa",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			got := metadatatest.NewCellID(in)
			if got != in {
				t.Fatalf("NewCellID(%q) = %q, want unchanged", in, got)
			}
		})
	}
}

func TestNewCellID_PanicsOnInvalid(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"kebab", "foo-bar"},
		{"uppercase", "FooBar"},
		{"single_char", "a"},
		{"leading_digit", "1foo"},
		{"underscore", "foo_bar"},
		{"empty", ""},
		{"dot", "foo.bar"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("NewCellID(%q) did not panic", tc.in)
				}
				err, ok := r.(*errcode.Error)
				if !ok {
					t.Fatalf("NewCellID(%q) panic value type = %T, want *errcode.Error", tc.in, r)
				}
				if err.Code != errcode.ErrInternal {
					t.Fatalf("NewCellID(%q) panic err.Code = %s, want %s", tc.in, err.Code, errcode.ErrInternal)
				}
				if !strings.HasPrefix(err.Message, "metadatatest: invalid cell id ") {
					t.Fatalf("NewCellID(%q) panic message = %q, want prefix %q", tc.in, err.Message, "metadatatest: invalid cell id ")
				}
			}()
			_ = metadatatest.NewCellID(tc.in)
		})
	}
}

func TestPredefinedConstants(t *testing.T) {
	// init-time NewCellID validation already gates the constant assignments.
	// This test pins the literal values so future renames cannot silently
	// drift away from kernel/governance fixture expectations.
	pinned := map[string]string{
		"CellIDAccessCore":     metadatatest.CellIDAccessCore,
		"CellIDAuditCore":      metadatatest.CellIDAuditCore,
		"CellIDBillingCore":    metadatatest.CellIDBillingCore,
		"CellIDConfigCore":     metadatatest.CellIDConfigCore,
		"CellIDSharedCrypto":   metadatatest.CellIDSharedCrypto,
		"CellIDSharedValidate": metadatatest.CellIDSharedValidate,
		"CellIDFoobar":         metadatatest.CellIDFoobar,
		"CellIDAA":             metadatatest.CellIDAA,
	}
	want := map[string]string{
		"CellIDAccessCore":     "accesscore",
		"CellIDAuditCore":      "auditcore",
		"CellIDBillingCore":    "billingcore",
		"CellIDConfigCore":     "configcore",
		"CellIDSharedCrypto":   "sharedcrypto",
		"CellIDSharedValidate": "sharedvalidate",
		"CellIDFoobar":         "foobar",
		"CellIDAA":             "aa",
	}
	for k, got := range pinned {
		if got != want[k] {
			t.Errorf("%s = %q, want %q", k, got, want[k])
		}
	}
}
