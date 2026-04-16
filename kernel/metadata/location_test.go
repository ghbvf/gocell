package metadata

import (
	"testing"

	"gopkg.in/yaml.v3"
)

// TestPosition_ZeroValue ensures a zero Position is distinguishable from
// a real (1-based) yaml location.
func TestPosition_ZeroValue(t *testing.T) {
	var p Position
	if p.Line != 0 || p.Column != 0 {
		t.Errorf("zero Position = %+v, want {0 0}", p)
	}
	if p.Known() {
		t.Errorf("zero Position.Known() = true, want false")
	}

	q := Position{Line: 3, Column: 5}
	if !q.Known() {
		t.Errorf("{3 5}.Known() = false, want true")
	}
}

// TestFind_RootMapping verifies that Find descends into a DocumentNode
// and then walks a MappingNode by field name.
func TestFind_RootMapping(t *testing.T) {
	src := `
id: access-core
type: core
`
	root := mustParseNode(t, src)

	n, err := Find(root, "id")
	if err != nil {
		t.Fatalf("Find(id) err = %v", err)
	}
	if n.Value != "access-core" {
		t.Errorf("Find(id).Value = %q, want access-core", n.Value)
	}
	if n.Line != 2 {
		t.Errorf("Find(id).Line = %d, want 2", n.Line)
	}
}

// TestFind_NestedField verifies dot-separated mapping traversal.
func TestFind_NestedField(t *testing.T) {
	src := `
id: access-core
owner:
  team: platform
  role: backend
`
	root := mustParseNode(t, src)

	n, err := Find(root, "owner.team")
	if err != nil {
		t.Fatalf("Find(owner.team) err = %v", err)
	}
	if n.Value != "platform" {
		t.Errorf("Find(owner.team).Value = %q, want platform", n.Value)
	}
	if n.Line != 4 {
		t.Errorf("Find(owner.team).Line = %d, want 4", n.Line)
	}
}

// TestFind_ArrayIndex verifies that [n] indexes into a SequenceNode.
func TestFind_ArrayIndex(t *testing.T) {
	src := `
cells:
  - access-core
  - audit-core
  - config-core
`
	root := mustParseNode(t, src)

	n, err := Find(root, "cells[1]")
	if err != nil {
		t.Fatalf("Find(cells[1]) err = %v", err)
	}
	if n.Value != "audit-core" {
		t.Errorf("Find(cells[1]).Value = %q, want audit-core", n.Value)
	}
	if n.Line != 4 {
		t.Errorf("Find(cells[1]).Line = %d, want 4", n.Line)
	}
}

// TestFind_NestedArrayWithField verifies deep paths mixing fields + indices.
func TestFind_NestedArrayWithField(t *testing.T) {
	src := `
slices:
  - id: session-login
    contractUsages:
      - contract: http.auth.login.v1
        role: serve
      - contract: event.session.created.v1
        role: publish
  - id: session-validate
    contractUsages:
      - contract: http.auth.validate.v1
        role: serve
`
	root := mustParseNode(t, src)

	n, err := Find(root, "slices[0].contractUsages[1].contract")
	if err != nil {
		t.Fatalf("Find err = %v", err)
	}
	if n.Value != "event.session.created.v1" {
		t.Errorf("Find(...).Value = %q", n.Value)
	}

	n, err = Find(root, "slices[1].contractUsages[0].contract")
	if err != nil {
		t.Fatalf("Find err = %v", err)
	}
	if n.Value != "http.auth.validate.v1" {
		t.Errorf("Find(...).Value = %q", n.Value)
	}
}

// TestFind_FieldNotFound returns a clear error.
func TestFind_FieldNotFound(t *testing.T) {
	src := `id: access-core`
	root := mustParseNode(t, src)

	if _, err := Find(root, "nope"); err == nil {
		t.Errorf("Find(nope) err = nil, want not found")
	}
}

// TestFind_IndexOutOfRange returns a clear error.
func TestFind_IndexOutOfRange(t *testing.T) {
	src := `
cells:
  - a
  - b
`
	root := mustParseNode(t, src)

	if _, err := Find(root, "cells[5]"); err == nil {
		t.Errorf("Find(cells[5]) err = nil, want out of range")
	}
}

// TestFind_TypeMismatch (indexing into a mapping).
func TestFind_TypeMismatch(t *testing.T) {
	src := `
owner:
  team: platform
`
	root := mustParseNode(t, src)

	if _, err := Find(root, "owner[0]"); err == nil {
		t.Errorf("Find(owner[0]) err = nil, want type mismatch")
	}
}

// TestFind_EmptyDocument returns an error / empty-doc sentinel.
func TestFind_EmptyDocument(t *testing.T) {
	src := ``
	root := mustParseNode(t, src)

	if _, err := Find(root, "id"); err == nil {
		t.Errorf("Find on empty doc err = nil, want error")
	}
}

// TestFind_InvalidPath rejects malformed paths early.
func TestFind_InvalidPath(t *testing.T) {
	src := `id: x`
	root := mustParseNode(t, src)

	cases := []string{
		"",
		".",
		"a.",
		".a",
		"a[",
		"a[]",
		"a[b]",  // non-numeric index
		"a[-1]", // negative index
		"a..b",
		"1a", // ident cannot start with digit
	}
	for _, p := range cases {
		if _, err := Find(root, p); err == nil {
			t.Errorf("Find(%q) err = nil, want invalid-path error", p)
		}
	}
}

// TestFind_MultiIndex supports a[0][1] (nested sequences).
func TestFind_MultiIndex(t *testing.T) {
	src := `
matrix:
  - - aa
    - ab
  - - ba
    - bb
`
	root := mustParseNode(t, src)

	n, err := Find(root, "matrix[0][1]")
	if err != nil {
		t.Fatalf("Find err = %v", err)
	}
	if n.Value != "ab" {
		t.Errorf("Find(matrix[0][1]).Value = %q, want ab", n.Value)
	}
}

// TestLocate is the convenience wrapper returning (Line, Column) via Position.
func TestLocate(t *testing.T) {
	src := `
id: access-core
owner:
  team: platform
`
	root := mustParseNode(t, src)

	pos := Locate(root, "owner.team")
	if !pos.Known() || pos.Line != 4 {
		t.Errorf("Locate(owner.team) = %+v, want known with Line=4", pos)
	}

	// Not found → zero value (swallowed error), Known()==false.
	pos = Locate(root, "nope")
	if pos.Known() {
		t.Errorf("Locate(nope) = %+v, want unknown", pos)
	}
}

// TestFind_EmptyMapping covers the boundary where a mapping exists but has
// no entries ({}). stepField must report "not found" cleanly — no panic on
// an empty Content slice.
func TestFind_EmptyMapping(t *testing.T) {
	src := `foo: {}`
	root := mustParseNode(t, src)

	// foo itself resolves to an empty MappingNode.
	n, err := Find(root, "foo")
	if err != nil {
		t.Fatalf("Find(foo) err = %v", err)
	}
	if n == nil {
		t.Fatal("Find(foo) returned nil")
	}

	// foo.bar must fail with "not found", not panic.
	if _, err := Find(root, "foo.bar"); err == nil {
		t.Errorf("Find(foo.bar) in empty mapping err = nil, want not found")
	}
}

// TestFind_IdentNames accepts letters/digits/underscore/dash after first char.
func TestFind_IdentNames(t *testing.T) {
	src := `
my_field: a
my-field: b
Field2: c
`
	root := mustParseNode(t, src)

	for _, k := range []string{"my_field", "my-field", "Field2"} {
		if _, err := Find(root, k); err != nil {
			t.Errorf("Find(%q) err = %v, want ok", k, err)
		}
	}
}

// mustParseNode parses src as a single yaml document and returns the root
// node (DocumentNode). t.Fatal on error.
func mustParseNode(t *testing.T, src string) *yaml.Node {
	t.Helper()
	var n yaml.Node
	if err := yaml.Unmarshal([]byte(src), &n); err != nil {
		t.Fatalf("parse: %v", err)
	}
	return &n
}
