// canonical.go provides a deterministic, stable binary encoding for arbitrary
// Go values used to derive fingerprints from struct metadata.
//
// Design constraints:
//   - struct fields are visited in sorted field-name order (not declaration order)
//     so that field reordering in source does not change the encoding
//   - nil pointer and zero value are distinguishable
//   - fields tagged with fingerprint:"-" are skipped (documents, not structure)
//   - maps are visited in sorted key order
//   - slices include a length prefix so [] and nil are both distinguishable from
//     single-element slices
//   - encoding/json is NOT used: its map-key/field ordering is not guaranteed to
//     be stable across all Go versions and does not support sorted struct fields
package assembly

import (
	"fmt"
	"io"
	"reflect"
	"sort"
)

// canonicalEncode writes a deterministic binary representation of v to w.
// The encoding is designed for fingerprinting, not for deserialization.
//
// Type prefixes used in the wire format:
//
//	S:<len>:<bytes>   — string
//	I:<n>             — int64
//	U:<n>             — uint64
//	F:<s>             — float64 (via %g)
//	B:<0|1>           — bool
//	N                 — nil pointer / nil interface
//	P                 — non-nil pointer (followed by dereferenced value)
//	A:<len>           — array/slice length header (followed by len elements)
//	M:<len>           — map length header (followed by sorted key-value pairs)
//	{<len>            — struct open (field count)
//	K:<len>:<name>    — struct field name
//	}                 — struct close
func canonicalEncode(w io.Writer, v any) error {
	return encodeValue(w, reflect.ValueOf(v))
}

func encodeValue(w io.Writer, v reflect.Value) error { //nolint:cyclop,gocognit
	// Dereference interfaces to the concrete type.
	if v.Kind() == reflect.Interface {
		if v.IsNil() {
			_, err := fmt.Fprint(w, "N")
			return err
		}
		v = v.Elem()
	}

	switch v.Kind() {
	case reflect.Invalid:
		_, err := fmt.Fprint(w, "N")
		return err

	case reflect.Ptr:
		if v.IsNil() {
			_, err := fmt.Fprint(w, "N")
			return err
		}
		if _, err := fmt.Fprint(w, "P"); err != nil {
			return err
		}
		return encodeValue(w, v.Elem())

	case reflect.String:
		s := v.String()
		_, err := fmt.Fprintf(w, "S:%d:%s", len(s), s)
		return err

	case reflect.Bool:
		b := 0
		if v.Bool() {
			b = 1
		}
		_, err := fmt.Fprintf(w, "B:%d", b)
		return err

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		_, err := fmt.Fprintf(w, "I:%d", v.Int())
		return err

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		_, err := fmt.Fprintf(w, "U:%d", v.Uint())
		return err

	case reflect.Float32, reflect.Float64:
		_, err := fmt.Fprintf(w, "F:%g", v.Float())
		return err

	case reflect.Slice:
		if v.IsNil() {
			_, err := fmt.Fprint(w, "N")
			return err
		}
		return encodeSequence(w, v)

	case reflect.Array:
		return encodeSequence(w, v)

	case reflect.Map:
		return encodeMap(w, v)

	case reflect.Struct:
		return encodeStruct(w, v)

	default:
		_, err := fmt.Fprintf(w, "?:%s", v.Type().String())
		return err
	}
}

func encodeSequence(w io.Writer, v reflect.Value) error {
	n := v.Len()
	if _, err := fmt.Fprintf(w, "A:%d", n); err != nil {
		return err
	}
	for i := range n {
		if err := encodeValue(w, v.Index(i)); err != nil {
			return err
		}
	}
	return nil
}

func encodeMap(w io.Writer, v reflect.Value) error {
	if v.IsNil() {
		_, err := fmt.Fprint(w, "N")
		return err
	}
	keys := v.MapKeys()
	// Sort keys as strings for determinism.
	sort.Slice(keys, func(i, j int) bool {
		return fmt.Sprint(keys[i]) < fmt.Sprint(keys[j])
	})
	if _, err := fmt.Fprintf(w, "M:%d", len(keys)); err != nil {
		return err
	}
	for _, k := range keys {
		if err := encodeValue(w, k); err != nil {
			return err
		}
		if err := encodeValue(w, v.MapIndex(k)); err != nil {
			return err
		}
	}
	return nil
}

// fingerprintFields returns the struct field indexes to encode, sorted by name.
// Fields tagged with `fingerprint:"-"` are excluded.
func fingerprintFields(t reflect.Type) []int {
	var idxs []int
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		if f.Tag.Get("fingerprint") == "-" {
			continue
		}
		idxs = append(idxs, i)
	}
	sort.Slice(idxs, func(a, b int) bool {
		return t.Field(idxs[a]).Name < t.Field(idxs[b]).Name
	})
	return idxs
}

func encodeStruct(w io.Writer, v reflect.Value) error {
	t := v.Type()
	idxs := fingerprintFields(t)
	if _, err := fmt.Fprintf(w, "{%d", len(idxs)); err != nil {
		return err
	}
	for _, i := range idxs {
		name := t.Field(i).Name
		if _, err := fmt.Fprintf(w, "K:%d:%s", len(name), name); err != nil {
			return err
		}
		if err := encodeValue(w, v.Field(i)); err != nil {
			return err
		}
	}
	_, err := fmt.Fprint(w, "}")
	return err
}
