// Package compliant is a fixture for MESSAGE-CONST-LITERAL-01 positive
// case: every errcode.New / errcode.Wrap call passes a const literal as
// the message argument. Parsed by archtest in pure-AST mode; not intended
// to compile.
package compliant

const localConstMessage = "compliant: domain rule"

// CallWithStringLiteral is the canonical compliant pattern.
func CallWithStringLiteral(err error) error {
	return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "compliant: literal message")
}

// CallWithPackageConst verifies that a package-level const ident is also
// accepted (no PII risk because the value is fixed at compile time).
func CallWithPackageConst() error {
	return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, localConstMessage)
}

// CallWrapWithLiteral verifies the same rule applies to errcode.Wrap.
func CallWrapWithLiteral(err error) error {
	return errcode.Wrap(errcode.KindInternal, errcode.ErrInternal, "compliant: wrap literal", err)
}

// CallWritePublicWithLiteral verifies the helper-coverage extension
// accepts compile-time const literals at httputil.WritePublic.
func CallWritePublicWithLiteral() {
	httputil.WritePublic(nil, nil, errcode.KindInvalid,
		errcode.ErrValidationFailed, "compliant: literal write public")
}

// CallWrapOrInfraWithLiteral verifies the helper-coverage extension
// accepts compile-time const literals at ctxcancel.WrapOrInfra.
func CallWrapOrInfraWithLiteral(cause error, op, id string) error {
	return ctxcancel.WrapOrInfra(cause, op, id, errcode.ErrInternal,
		"compliant: literal wrap or infra")
}
