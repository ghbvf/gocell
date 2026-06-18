//go:build archtest_fixture

// Package tomapfullcolumnsetfixture is the build-tagged RED/GREEN fixture for
// the PROJECTION-TOMAP-FULL-COLUMN-SET-01 archtest reverse self-check. It is
// loaded only under the archtest_fixture build tag (the literal must agree with
// the unexported fixtureBuildTag const in tools/archtest/fixture.go; Go build
// directives cannot reference a Go const). The tag excludes this package from
// `go build ./...` / `go test ./...`, so it never pollutes real-repo scans.
//
// # Cases covered
//
// The rule proves that a generated ToMap emits the FULL column set as a single
// `return map[string]any{ <one entry per field> }` literal with no conditional
// omission, and FLAGS any body that omits a column (the #2159 omitempty-fission
// regression that re-opens the masking presence side channel).
//
// GREEN (MUST NOT be flagged) — full column set, single literal:
//   - GoodItem — every field present, no `if`.
//
// RED (MUST be flagged) — a column can be omitted:
//   - BadOmitItem        — optional field added under `if` (omitempty fission).
//   - BadShortLiteralItem — literal emits fewer entries than the struct has fields.
//
// The shared detector toMapFullColumnSetDiags (projection_tomap_full_column_set_test.go)
// is run over this fixture: it must flag both RED types and neither GREEN control.
package tomapfullcolumnsetfixture

// GoodItem is the GREEN control: ToMap emits the full column set as a single
// map literal with no conditional.
type GoodItem struct {
	A string
	B string
}

// ToMap emits every field unconditionally — the canonical full-column-set shape.
func (i GoodItem) ToMap() map[string]any {
	return map[string]any{
		"a": i.A,
		"b": i.B,
	}
}

// BadOmitItem is a RED control: ToMap adds the optional column under an `if`
// (omitempty fission) — exactly the #2159 regression shape.
type BadOmitItem struct {
	A string
	B string
}

// ToMap conditionally omits B — must be flagged.
func (i BadOmitItem) ToMap() map[string]any {
	m := map[string]any{
		"a": i.A,
	}
	if i.B != "" {
		m["b"] = i.B
	}
	return m
}

// BadShortLiteralItem is a RED control: ToMap returns a single literal but with
// fewer entries than the struct has fields (a column silently dropped).
type BadShortLiteralItem struct {
	A string
	B string
}

// ToMap drops column B from the literal — must be flagged.
func (i BadShortLiteralItem) ToMap() map[string]any {
	return map[string]any{
		"a": i.A,
	}
}
