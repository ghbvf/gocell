package nilutil_test

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/nilutil"
)

// fakeIface is a stand-in interface used to construct typed-nil values.
type fakeIface interface {
	Do()
}

// fakePtr implements fakeIface via pointer receiver — typed-nil of *fakePtr is
// the canonical "interface holds a nil pointer" case.
type fakePtr struct{}

func (*fakePtr) Do() {}

func TestIsNil_BareNil(t *testing.T) {
	if !nilutil.IsNil(nil) {
		t.Fatal("IsNil(nil) must be true")
	}
}

func TestIsNil_TypedNilPointer(t *testing.T) {
	var p *fakePtr // typed nil
	var iface fakeIface = p
	if !nilutil.IsNil(iface) {
		t.Fatal("IsNil(typed-nil pointer) must be true")
	}
}

func TestIsNil_TypedNilMap(t *testing.T) {
	var m map[string]int
	if !nilutil.IsNil(any(m)) {
		t.Fatal("IsNil(nil map) must be true")
	}
}

func TestIsNil_TypedNilSlice(t *testing.T) {
	var s []int
	if !nilutil.IsNil(any(s)) {
		t.Fatal("IsNil(nil slice) must be true")
	}
}

func TestIsNil_TypedNilChan(t *testing.T) {
	var c chan int
	if !nilutil.IsNil(any(c)) {
		t.Fatal("IsNil(nil chan) must be true")
	}
}

func TestIsNil_TypedNilFunc(t *testing.T) {
	var f func()
	if !nilutil.IsNil(any(f)) {
		t.Fatal("IsNil(nil func) must be true")
	}
}

func TestIsNil_ValidPointer(t *testing.T) {
	p := &fakePtr{}
	var iface fakeIface = p
	if nilutil.IsNil(iface) {
		t.Fatal("IsNil(valid pointer) must be false")
	}
}

func TestIsNil_ValidMap(t *testing.T) {
	m := map[string]int{}
	if nilutil.IsNil(any(m)) {
		t.Fatal("IsNil(empty non-nil map) must be false")
	}
}

func TestIsNil_NonNilableKinds(t *testing.T) {
	cases := []struct {
		name string
		v    any
	}{
		{"int zero", 0},
		{"int positive", 42},
		{"empty string", ""},
		{"non-empty string", "hello"},
		{"struct zero", struct{ X int }{}},
		{"bool false", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if nilutil.IsNil(tc.v) {
				t.Fatalf("IsNil(%v) must be false for non-nilable kind", tc.v)
			}
		})
	}
}
