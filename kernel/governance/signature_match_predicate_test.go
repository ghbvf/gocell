package governance

import (
	"go/token"
	"go/types"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSignatureMatchesValidationResultEmitter_CrossPackageRejected pins
// the same-package owner guard: a method whose receiver type and result
// type live in different packages is NOT a ValidationResult emitter, even
// when the result type happens to share the name "ValidationResult".
//
// This branch (recvNamed.Obj().Pkg().Path() == r0.Obj().Pkg().Path()) is
// not reachable via the BFS fixture path because types.Config.Check on a
// single in-memory file produces only one *types.Package — fixture tests
// inherently can't synthesize cross-package shapes. We exercise it here
// by composing *types.Signature directly via go/types constructors.
func TestSignatureMatchesValidationResultEmitter_CrossPackageRejected(t *testing.T) {
	t.Parallel()

	// Package A holds the ValidationResult result type.
	pkgTarget := types.NewPackage("targetpkg", "targetpkg")
	vrName := types.NewTypeName(token.NoPos, pkgTarget, "ValidationResult", nil)
	vrNamed := types.NewNamed(vrName, types.NewStruct(nil, nil), nil)
	pkgTarget.Scope().Insert(vrName)

	// Package A also holds RuleCode.
	rcName := types.NewTypeName(token.NoPos, pkgTarget, "RuleCode", nil)
	rcNamed := types.NewNamed(rcName, types.Typ[types.String], nil)
	pkgTarget.Scope().Insert(rcName)

	// Package B holds the receiver type X.
	pkgConsumer := types.NewPackage("consumerpkg", "consumerpkg")
	xName := types.NewTypeName(token.NoPos, pkgConsumer, "X", nil)
	xNamed := types.NewNamed(xName, types.NewStruct(nil, nil), nil)
	pkgConsumer.Scope().Insert(xName)

	// Signature: (x consumerpkg.X) emit(code targetpkg.RuleCode) targetpkg.ValidationResult
	// The result is in targetpkg but receiver is in consumerpkg — cross-package.
	recv := types.NewVar(token.NoPos, pkgConsumer, "x", xNamed)
	p0 := types.NewVar(token.NoPos, pkgTarget, "code", rcNamed)
	r0 := types.NewVar(token.NoPos, pkgTarget, "", vrNamed)
	sig := types.NewSignatureType(recv, nil, nil,
		types.NewTuple(p0), types.NewTuple(r0), false)

	assert.False(t, signatureMatchesValidationResultEmitter(sig, xNamed),
		"cross-package shape must be rejected by the same-pkg owner guard")
}

// TestSignatureMatchesValidationResultEmitter_SamePackageAccepted is the
// positive twin: both receiver and result in the same synthetic package,
// with param 0 typed as RuleCode (not plain string), satisfies all three
// predicates (arg0 RuleCode, return ValidationResult, same-package owner).
// Together with the Rejected case, these two tests pin the guard at the
// function boundary, independent of BFS plumbing.
func TestSignatureMatchesValidationResultEmitter_SamePackageAccepted(t *testing.T) {
	t.Parallel()

	pkg := types.NewPackage("p", "p")
	vrName := types.NewTypeName(token.NoPos, pkg, "ValidationResult", nil)
	vrNamed := types.NewNamed(vrName, types.NewStruct(nil, nil), nil)
	pkg.Scope().Insert(vrName)

	// RuleCode must live in the same package as ValidationResult to pass the
	// same-pkg owner check.
	rcName := types.NewTypeName(token.NoPos, pkg, "RuleCode", nil)
	rcNamed := types.NewNamed(rcName, types.Typ[types.String], nil)
	pkg.Scope().Insert(rcName)

	xName := types.NewTypeName(token.NoPos, pkg, "X", nil)
	xNamed := types.NewNamed(xName, types.NewStruct(nil, nil), nil)
	pkg.Scope().Insert(xName)

	recv := types.NewVar(token.NoPos, pkg, "x", xNamed)
	p0 := types.NewVar(token.NoPos, pkg, "code", rcNamed)
	r0 := types.NewVar(token.NoPos, pkg, "", vrNamed)
	sig := types.NewSignatureType(recv, nil, nil,
		types.NewTuple(p0), types.NewTuple(r0), false)

	assert.True(t, signatureMatchesValidationResultEmitter(sig, xNamed),
		"same-package emitter shape must be accepted")
}
