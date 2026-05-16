//go:build archtest_fixture

// Package yamlquotefixture provides a build-tag-gated fixture for the
// YAML-QUOTE-FUNNEL-01 reverse self-test. The file is compiled only under
// -tags=archtest_fixture, so production builds never see the alias bypass
// shape encoded here.
package yamlquotefixture

import "github.com/ghbvf/gocell/pkg/yamlsafe"

// AliasOfScalar is a true type alias of yamlsafe.Scalar. Conversions through
// this alias must still be flagged by YAML-QUOTE-FUNNEL-01 — the archtest
// resolves alias underlying types via types.Unalias.
type AliasOfScalar = yamlsafe.Scalar

// BypassViaAlias is the violation site: a bare string-variable conversion
// routed through an alias of Scalar. Archtest must detect this as a YAML
// quoting funnel bypass. Note: string *variable* (not string literal) is used
// so that info.TypeOf(arg) reports the source type (string), not the
// conversion-target type — a string literal in a conversion context is
// contextually typed to the target, which would cause allowedScalarConversionArg
// to misidentify it as "already typed as Scalar" and skip the violation.
func BypassViaAlias(raw string) AliasOfScalar {
	return AliasOfScalar(raw) //nolint:staticcheck // intentional bypass fixture
}

// CompliantQuoted is the negative control: conversion of yamlsafe.Quote(...).
// Archtest must NOT flag this.
func CompliantQuoted() AliasOfScalar {
	return AliasOfScalar(yamlsafe.Quote("safe"))
}

// BypassViaLiteral is a second violation site: a string literal directly
// wrapped in the Scalar conversion. Without const-value detection in
// allowedScalarConversionArg, Go's contextual typing would assign the
// target type (Scalar) to the literal, causing the scanner to misidentify
// it as "already typed as Scalar" and silently allow the bypass. Archtest
// must flag this exactly like the variable-arg case.
func BypassViaLiteral() yamlsafe.Scalar {
	return yamlsafe.Scalar("evil-static-literal") //nolint:staticcheck // intentional bypass fixture
}

// BypassViaConstConcat exercises the next degree of constant evaluation:
// a const expression "a"+"b" still resolves to a constant value under
// types.Info.Types, so the same defense applies.
func BypassViaConstConcat() yamlsafe.Scalar {
	return yamlsafe.Scalar("evil-" + "concat") //nolint:staticcheck // intentional bypass fixture
}

// CompliantTypedScalar is a negative control: a yamlsafe.Scalar-typed
// local variable feeding through an identity conversion (this is the
// "already-Scalar" allowed path, distinct from the const-typing
// misclassification fixed by the const-value detector).
func CompliantTypedScalar() yamlsafe.Scalar {
	s := yamlsafe.Quote("via-helper")
	return yamlsafe.Scalar(s) // identity conversion, OK
}
