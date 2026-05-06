package metadata

import (
	"fmt"

	"gopkg.in/yaml.v3"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// GoIdentifier is a string statically guaranteed to satisfy
// GoStructNamePattern (^[A-Z][A-Za-z0-9]*$). The unexported value field
// forces all construction through NewGoIdentifier or UnmarshalYAML, so any
// GoIdentifier value reaching codegen templates has already been validated.
//
// Raw strings cannot be coerced to GoIdentifier; downstream consumers call
// String() to obtain the validated text. This is the typed-identifier
// boundary (review §R1) preventing arbitrary cell.yaml values from being
// embedded into emitted Go source code.
type GoIdentifier struct{ value string }

// NewGoIdentifier validates s and returns a typed GoIdentifier. Empty input
// returns the zero value (cells that opt out of K#04 codegen leave
// goStructName empty in cell.yaml); callers requiring a non-empty value must
// check IsZero separately. Non-empty input that does not satisfy
// GoStructNamePattern is rejected with errcode.ErrMetadataInvalid.
func NewGoIdentifier(s string) (GoIdentifier, error) {
	if s == "" {
		return GoIdentifier{}, nil
	}
	if !goStructNameRe.MatchString(s) {
		return GoIdentifier{}, errcode.New(errcode.KindInvalid, errcode.ErrMetadataInvalid,
			"goStructName must match GoStructNamePattern (uppercase ASCII letter start, ASCII letters + digits)",
			errcode.WithInternal(fmt.Sprintf("value=%q pattern=%s", s, GoStructNamePattern)))
	}
	return GoIdentifier{value: s}, nil
}

// MustNewGoIdentifier is the panic-on-misconfig variant of NewGoIdentifier
// for composition-root and codegen-emitted literals. cell_gen.go embeds the
// validated goStructName from the parsed CellMeta — if a manual edit
// introduces an invalid value, the resulting panic is the expected
// fail-fast (C-class initialization error, see error-handling.md panic
// taxonomy). Runtime production code must use NewGoIdentifier.
func MustNewGoIdentifier(s string) GoIdentifier {
	g, err := NewGoIdentifier(s)
	if err != nil {
		panic(err) // C-class: codegen-emitted literal proven invalid at init
	}
	return g
}

// String returns the underlying identifier text. Safe to embed into Go code
// generation templates because every constructed value has been validated.
func (g GoIdentifier) String() string { return g.value }

// IsZero reports whether the identifier is the zero value (empty input,
// indicating a cell that has not opted into K#04 codegen).
func (g GoIdentifier) IsZero() bool { return g.value == "" }

// MarshalYAML emits the underlying string so YAML round-trips preserve the
// original cell.yaml content.
func (g GoIdentifier) MarshalYAML() (any, error) { return g.value, nil }

// UnmarshalYAML is invoked by parser.go when CellMeta.GoStructName is
// decoded. Validation happens at parse time so downstream derivation,
// governance, and codegen all operate on validated values.
func (g *GoIdentifier) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err != nil {
		return err
	}
	parsed, err := NewGoIdentifier(s)
	if err != nil {
		return err
	}
	*g = parsed
	return nil
}
