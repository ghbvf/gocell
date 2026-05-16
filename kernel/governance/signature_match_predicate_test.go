package governance

import (
	"go/token"
	"go/types"
	"testing"

	"github.com/stretchr/testify/assert"
)

// synthesizeEmitterGateForTest constructs an emitterGate plus a synthetic
// locator named type in a single package, returning all three so the
// two predicate tests can either reuse the locator as the receiver
// (Accepted path) or synthesize a foreign receiver (Rejected path).
//
// Predicate body uses types.Identical(recvNamed, gate.locatorType), so
// distinguishing the two cases reduces to "pass locatorNamed as
// recvNamed" vs "pass a different *types.Named".
func synthesizeEmitterGateForTest() (locatorNamed *types.Named, gate emitterGate, pkg *types.Package) {
	pkg = types.NewPackage("p", "p")

	rcName := types.NewTypeName(token.NoPos, pkg, "RuleCode", nil)
	rcNamed := types.NewNamed(rcName, types.Typ[types.String], nil)
	pkg.Scope().Insert(rcName)

	vrName := types.NewTypeName(token.NoPos, pkg, "ValidationResult", nil)
	vrNamed := types.NewNamed(vrName, types.NewStruct(nil, nil), nil)
	pkg.Scope().Insert(vrName)

	locatorName := types.NewTypeName(token.NoPos, pkg, "locator", nil)
	locatorNamed = types.NewNamed(locatorName, types.NewStruct(nil, nil), nil)
	pkg.Scope().Insert(locatorName)

	gate = emitterGate{
		ruleCodeType:         rcNamed,
		validationResultType: vrNamed,
		locatorType:          locatorNamed,
	}
	return locatorNamed, gate, pkg
}

// synthesizeEmitterSig builds a (*recv) emit(code RuleCode)
// ValidationResult signature against the synthetic emitterGate, suitable
// for feeding signatureMatchesValidationResultEmitter.
func synthesizeEmitterSig(pkg *types.Package, recvNamed *types.Named, gate emitterGate) *types.Signature {
	recv := types.NewVar(token.NoPos, pkg, "x", types.NewPointer(recvNamed))
	p0 := types.NewVar(token.NoPos, pkg, "code", gate.ruleCodeType)
	r0 := types.NewVar(token.NoPos, pkg, "", gate.validationResultType)
	return types.NewSignatureType(recv, nil, nil,
		types.NewTuple(p0), types.NewTuple(r0), false)
}

// TestSignatureMatchesValidationResultEmitter_NonLocatorReceiverRejected
// pins the R2-P1 owner gate: a method whose signature fully matches the
// emitter shape (param 0 type-identical to RuleCode, result 0 type-
// identical to ValidationResult, non-variadic, single result) but whose
// receiver is not types.Identical to the locator type must be rejected.
//
// This covers both threat surfaces R2-P1 closes:
//
//  1. Unrelated same-package type (analogous to Helper in
//     unrelated_receiver_with_emitter_shape_ignored_RED).
//  2. Outer type that embeds locator and inherits methods via promotion
//     (analogous to Validator in embedded_locator_outer_receiver_ignored_RED).
//
// At the predicate level both reduce to "recvNamed is a different
// *types.Named than gate.locatorType" — types.Identical returns false.
// Method promotion does not change *types.Named identity, so this gate
// is structurally immune to the embedding attack surface that defeated
// the prior marker-iface design (PR #521 review F-1).
func TestSignatureMatchesValidationResultEmitter_NonLocatorReceiverRejected(t *testing.T) {
	t.Parallel()

	_, gate, pkg := synthesizeEmitterGateForTest()

	xName := types.NewTypeName(token.NoPos, pkg, "X", nil)
	xNamed := types.NewNamed(xName, types.NewStruct(nil, nil), nil)
	pkg.Scope().Insert(xName)

	sig := synthesizeEmitterSig(pkg, xNamed, gate)

	assert.False(t, signatureMatchesValidationResultEmitter(sig, xNamed, gate),
		"shape-matched non-locator receiver must be rejected by types.Identical owner gate")
}

// TestSignatureMatchesValidationResultEmitter_LocatorReceiverAccepted is
// the positive twin: same emitter shape, recvNamed is the locator named
// type itself, so types.Identical returns true and the predicate accepts.
//
// Together with the Rejected test these two pin the gate transitions at
// the function boundary, independent of BFS plumbing.
func TestSignatureMatchesValidationResultEmitter_LocatorReceiverAccepted(t *testing.T) {
	t.Parallel()

	locatorNamed, gate, pkg := synthesizeEmitterGateForTest()

	sig := synthesizeEmitterSig(pkg, locatorNamed, gate)

	assert.True(t, signatureMatchesValidationResultEmitter(sig, locatorNamed, gate),
		"shape-matched locator receiver must be accepted by types.Identical owner gate")
}
