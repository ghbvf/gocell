package assembly

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// encode is a tiny helper that runs canonicalEncode and returns the bytes.
func encode(t *testing.T, v any) string {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, canonicalEncode(&buf, v))
	return buf.String()
}

func TestCanonicalEncode_PrimitiveKinds(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"int", int(42), "I:42"},
		{"int8", int8(-3), "I:-3"},
		{"int16", int16(1000), "I:1000"},
		{"int32", int32(-1), "I:-1"},
		{"int64", int64(1 << 32), "I:4294967296"},
		{"uint", uint(7), "U:7"},
		{"uint8", uint8(255), "U:255"},
		{"uint16", uint16(65535), "U:65535"},
		{"uint32", uint32(1), "U:1"},
		{"uint64", uint64(1 << 40), "U:1099511627776"},
		{"float32", float32(1.5), "F:1.5"},
		{"float64", float64(-2.25), "F:-2.25"},
		{"bool true", true, "B:1"},
		{"bool false", false, "B:0"},
		{"string", "hello", "S:5:hello"},
		{"empty string", "", "S:0:"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, encode(t, tc.in))
		})
	}
}

func TestCanonicalEncode_NilSliceVsEmptySlice(t *testing.T) {
	var nilSlice []string
	emptySlice := []string{}

	nilEnc := encode(t, nilSlice)
	emptyEnc := encode(t, emptySlice)

	assert.Equal(t, "N", nilEnc, "nil slice should encode as N")
	assert.Equal(t, "A:0", emptyEnc, "empty slice should encode as A:0")
	assert.NotEqual(t, nilEnc, emptyEnc, "nil and empty slices must be distinguishable")
}

func TestCanonicalEncode_Array(t *testing.T) {
	arr := [3]int{1, 2, 3}
	got := encode(t, arr)
	assert.Equal(t, "A:3I:1I:2I:3", got)
}

func TestCanonicalEncode_NilMapVsEmptyMap(t *testing.T) {
	var nilMap map[string]int
	emptyMap := map[string]int{}

	nilEnc := encode(t, nilMap)
	emptyEnc := encode(t, emptyMap)

	assert.Equal(t, "N", nilEnc)
	assert.Equal(t, "M:0", emptyEnc)
	assert.NotEqual(t, nilEnc, emptyEnc)
}

func TestCanonicalEncode_MapKeySortDeterministic(t *testing.T) {
	// Two maps with identical entries inserted in different orders must encode identically.
	m1 := map[string]string{"a": "1", "b": "2", "c": "3"}
	m2 := map[string]string{"c": "3", "a": "1", "b": "2"}
	assert.Equal(t, encode(t, m1), encode(t, m2))
}

func TestCanonicalEncode_MapWithIntKeys(t *testing.T) {
	// Integer keys are sorted lexicographically by their %v string form.
	m := map[int]string{2: "two", 10: "ten", 1: "one"}
	got := encode(t, m)
	// "1" < "10" < "2" lexicographically, so the order is 1, 10, 2.
	assert.Equal(t, "M:3I:1S:3:oneI:10S:3:tenI:2S:3:two", got)
}

func TestCanonicalEncode_NilInterface(t *testing.T) {
	var i any = nil
	assert.Equal(t, "N", encode(t, i))
}

func TestCanonicalEncode_NonNilInterfaceUnwraps(t *testing.T) {
	var i any = "wrapped"
	assert.Equal(t, "S:7:wrapped", encode(t, i))
}

func TestCanonicalEncode_PointerNonNilWritesPDeref(t *testing.T) {
	v := 42
	got := encode(t, &v)
	assert.Equal(t, "PI:42", got)
}

func TestCanonicalEncode_NilPointer(t *testing.T) {
	var p *int
	assert.Equal(t, "N", encode(t, p))
}

func TestCanonicalEncode_StructFieldsSortedByName(t *testing.T) {
	// Fields declared in order Z, A, M but encoded sorted by name: A, M, Z.
	type s struct {
		Z int
		A int
		M int
	}
	got := encode(t, s{Z: 1, A: 2, M: 3})
	// A=2, M=3, Z=1
	assert.Equal(t, "{3K:1:AI:2K:1:MI:3K:1:ZI:1}", got)
}

func TestCanonicalEncode_StructUnexportedFieldsSkipped(t *testing.T) {
	type s struct {
		Pub  int
		priv int
	}
	got := encode(t, s{Pub: 7, priv: 99})
	assert.Equal(t, "{1K:3:PubI:7}", got)
	assert.NotContains(t, got, "priv")
}

func TestCanonicalEncode_StructFingerprintTagSkipsField(t *testing.T) {
	type s struct {
		Stable  string
		Ignored string `fingerprint:"-"`
	}
	a := encode(t, s{Stable: "x", Ignored: "yes"})
	b := encode(t, s{Stable: "x", Ignored: "no"})
	assert.Equal(t, a, b, "fingerprint:\"-\" tag must exclude field from encoding")
	assert.NotContains(t, a, "Ignored")
}

func TestCanonicalEncode_NestedPointerInStruct(t *testing.T) {
	type inner struct{ V int }
	type outer struct {
		P *inner
	}

	withNil := encode(t, outer{P: nil})
	withVal := encode(t, outer{P: &inner{V: 5}})

	assert.Contains(t, withNil, "K:1:PN")
	assert.Contains(t, withVal, "K:1:PP{1K:1:VI:5}")
	assert.NotEqual(t, withNil, withVal)
}

func TestCanonicalEncode_DefaultUnknownKindFallback(t *testing.T) {
	// reflect.Chan / reflect.Func fall through to the default branch.
	ch := make(chan int)
	got := encode(t, ch)
	// The default branch writes ?:<TypeString>.
	assert.True(t, strings.HasPrefix(got, "?:"), "got %q", got)
}

func TestCanonicalEncode_NestedStructAndSlice(t *testing.T) {
	// Compound type — a slice of structs with mixed fields — exercises
	// encodeStruct + encodeSequence + encodeValue together.
	type item struct {
		Name string
		N    int
	}
	got := encode(t, []item{{Name: "a", N: 1}, {Name: "b", N: 2}})
	// Each struct has 2 fields encoded sorted: N, then Name.
	assert.Equal(t, "A:2{2K:1:NI:1K:4:NameS:1:a}{2K:1:NI:2K:4:NameS:1:b}", got)
}

// errWriter forces every Write to fail; lets us exercise the error-return
// branches in canonicalEncode without filesystem tricks.
type errWriter struct{ err error }

func (w *errWriter) Write(p []byte) (int, error) { return 0, w.err }

func TestCanonicalEncode_PropagatesWriterErrors(t *testing.T) {
	type s struct {
		A   string
		Sub struct{ B int }
		Lst []int
		M   map[string]int
		P   *int
	}
	v := 1
	val := s{A: "x", Lst: []int{1}, M: map[string]int{"k": 2}, P: &v}
	val.Sub.B = 9

	for _, kind := range []string{
		"string", "struct", "slice", "map", "pointer",
	} {
		t.Run(kind, func(t *testing.T) {
			err := canonicalEncode(&errWriter{err: assert.AnError}, val)
			require.Error(t, err)
		})
	}
}
