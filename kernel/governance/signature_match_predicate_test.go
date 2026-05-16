package governance

import (
	"go/token"
	"go/types"
	"testing"

	"github.com/stretchr/testify/assert"
)

// synthesizeEmitterGateForTest constructs an emitterGate plus a receiver
// named X in a single synthetic package, returning the receiver named
// type and gate. Both predicate-tests below share this scaffolding;
// MarkerImplementedAccepted additionally injects the marker method into
// *X's method set, while MarkerNotImplementedRejected leaves it absent.
func synthesizeEmitterGateForTest() (xNamed *types.Named, gate emitterGate, pkg *types.Package) {
	pkg = types.NewPackage("p", "p")

	rcName := types.NewTypeName(token.NoPos, pkg, "RuleCode", nil)
	rcNamed := types.NewNamed(rcName, types.Typ[types.String], nil)
	pkg.Scope().Insert(rcName)

	vrName := types.NewTypeName(token.NoPos, pkg, "ValidationResult", nil)
	vrNamed := types.NewNamed(vrName, types.NewStruct(nil, nil), nil)
	pkg.Scope().Insert(vrName)

	ifaceMethodSig := types.NewSignatureType(nil, nil, nil, nil, nil, false)
	ifaceMethod := types.NewFunc(token.NoPos, pkg, "isValidationResultEmitter", ifaceMethodSig)
	iface := types.NewInterfaceType([]*types.Func{ifaceMethod}, nil)
	iface.Complete()

	gate = emitterGate{
		ruleCodeType:         rcNamed,
		validationResultType: vrNamed,
		emitterIface:         iface,
	}

	xName := types.NewTypeName(token.NoPos, pkg, "X", nil)
	xNamed = types.NewNamed(xName, types.NewStruct(nil, nil), nil)
	pkg.Scope().Insert(xName)

	return xNamed, gate, pkg
}

// synthesizeEmitterSig builds a (recv *X) emit(code RuleCode)
// ValidationResult signature against the synthetic emitterGate, suitable
// for feeding signatureMatchesValidationResultEmitter.
func synthesizeEmitterSig(pkg *types.Package, xNamed *types.Named, gate emitterGate) *types.Signature {
	recv := types.NewVar(token.NoPos, pkg, "x", types.NewPointer(xNamed))
	p0 := types.NewVar(token.NoPos, pkg, "code", gate.ruleCodeType)
	r0 := types.NewVar(token.NoPos, pkg, "", gate.validationResultType)
	return types.NewSignatureType(recv, nil, nil,
		types.NewTuple(p0), types.NewTuple(r0), false)
}

// TestSignatureMatchesValidationResultEmitter_MarkerNotImplementedRejected
// pins the post-R2-P1 sealed-marker owner gate: a method whose signature
// fully matches the emitter shape (param 0 type-identical to RuleCode,
// result 0 type-identical to ValidationResult, non-variadic, single
// result) but whose receiver does NOT implement the validationResultEmitter
// marker must be rejected.
//
// Pre-R2-P1 the equivalent rejection path was "cross-package owner"
// (recvNamed.Obj().Pkg() != ValidationResult.Obj().Pkg()) — that branch
// has been replaced wholesale by types.Implements, which becomes
// structurally impossible for non-marker types regardless of package.
// The new test composes a same-package shape match without injecting
// the marker method, asserting the marker gate fires.
func TestSignatureMatchesValidationResultEmitter_MarkerNotImplementedRejected(t *testing.T) {
	t.Parallel()

	xNamed, gate, pkg := synthesizeEmitterGateForTest()
	// Intentionally do NOT call xNamed.AddMethod — *X's method set is
	// empty, so types.Implements(*X, emitterIface) returns false.

	sig := synthesizeEmitterSig(pkg, xNamed, gate)

	assert.False(t, signatureMatchesValidationResultEmitter(sig, xNamed, gate),
		"shape-matched receiver lacking marker method must be rejected by Implements gate")
}

// TestSignatureMatchesValidationResultEmitter_MarkerImplementedAccepted
// is the positive twin: same emitter shape plus the
// isValidationResultEmitter() method injected on *X's method set, so
// types.Implements returns true and the predicate accepts.
//
// Together with the Rejected test these two pin the gate transitions at
// the function boundary, independent of BFS plumbing.
func TestSignatureMatchesValidationResultEmitter_MarkerImplementedAccepted(t *testing.T) {
	t.Parallel()

	xNamed, gate, pkg := synthesizeEmitterGateForTest()

	markerRecv := types.NewVar(token.NoPos, pkg, "_", types.NewPointer(xNamed))
	markerSig := types.NewSignatureType(markerRecv, nil, nil, nil, nil, false)
	xNamed.AddMethod(types.NewFunc(token.NoPos, pkg, "isValidationResultEmitter", markerSig))

	sig := synthesizeEmitterSig(pkg, xNamed, gate)

	assert.True(t, signatureMatchesValidationResultEmitter(sig, xNamed, gate),
		"shape-matched receiver implementing marker must be accepted by Implements gate")
}
