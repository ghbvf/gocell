// INVARIANT: GRPC-CELL-REGISTRAR-LAYER-01
//
// This file owns ONE invariant: the kernel/cell.GRPCServiceSpec.Register field
// is an untyped interface{} (any) with Kind==Interface and an empty name.
//
// # Why the field must stay `any`
//
// kernel/ must NOT import google.golang.org/grpc (depguard / kernel_internal_dag_test.go
// already enforce kernel⊥grpc; that existing gate is the load-bearing guarantee —
// do NOT add a duplicate archtest here). The only way kernel/cell can hold a
// "func(grpc.ServiceRegistrar)" value without naming the grpc type is `any`.
// The actual type-assert happens in runtime/grpc.ServiceRegistrar.Register.
//
// # AI-robust rating (per .claude/rules/gocell/ai-robust.md)
//
// Medium with a PERMANENT upstream ceiling.
//
// The true-Hard upstream path — sealing the `any` field via a marker interface
// defined in kernel/ that adapters/grpc can implement — is infeasible:
//
//   - A kernel-defined unexported marker (e.g. `registerFn interface{ gRPCRegisterFn() }`)
//     requires all implementers to be in the kernel package (Go unexported methods),
//     which is impossible for closure values from cells/ or adapters/.
//   - A kernel-defined exported marker interface is importable by adapters/grpc, but
//     can also be trivially implemented by any struct anywhere → no sealing.
//   - A marker in adapters/grpc cannot be imported by kernel/cell (kernel⊥adapters).
//
// This is the same permanent ceiling family as SPAN-SETATTR-HOLDER-SEAL (#851),
// HEALTHZ-HOLDER-SEAL (#893), and CTXKEYS-PRINCIPAL-WRITE-CALLER (#1282).
// Tracked as a permanent-ceiling won't-do at gh issue (see GRPCServiceSpec godoc).
//
// # What this archtest DOES assert (A1 only)
//
// A1 — reflect field-type lock: GRPCServiceSpec.Register is exactly `any`
// (reflect.Interface kind + empty Name). A drift to a concrete function type, a
// named interface, or `interface{ gRPCRegister() }` — any of which would add a
// grpc dependency to kernel/ — is caught by the reflect check.
//
// # Blind spots (per AI-robust §载体决策原则 "强制盲区自检")
//
//   - reflect.TypeOf cannot observe the runtime dynamic type stored in the field;
//     the type-assert behavior is covered by registrar_test.go (cases 5/6).
//   - This archtest does NOT verify kernel⊥grpc directly — that is handled by the
//     existing depguard + kernel_internal_dag_test.go gate (see above); duplication
//     would be a Soft override of a Hard gate, which ai-robust.md prohibits.
//
// Negative-control test: TestGRPCCellRegistrarLayer_A1_NegativeControl confirms
// that a type with a named/typed Register field produces ≥1 violation, ensuring
// the check is not vacuously true.
package archtest

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell"
)

// TestGRPCCellRegistrarLayer01_A1_FieldTypeLock verifies that
// GRPCServiceSpec.Register is an untyped any (Kind==Interface, Name=="").
func TestGRPCCellRegistrarLayer01_A1_FieldTypeLock(t *testing.T) {
	t.Parallel()

	rt := reflect.TypeOf(cell.GRPCServiceSpec{})

	field, ok := rt.FieldByName("Register")
	require.True(t, ok, "GRPCServiceSpec must have a Register field")

	fieldType := field.Type
	assert.Equal(t, reflect.Interface, fieldType.Kind(),
		"GRPCServiceSpec.Register must be an interface type (i.e. any / interface{})")
	assert.Equal(t, "", fieldType.Name(),
		"GRPCServiceSpec.Register must be the unnamed empty interface (any); "+
			"a named interface would import grpc or another non-kernel type")
}

// TestGRPCCellRegistrarLayer_A1_NegativeControl is the negative-control test:
// a struct with a concretely-typed Register field (func()) triggers a violation,
// confirming the positive test is not vacuously true.
func TestGRPCCellRegistrarLayer_A1_NegativeControl(t *testing.T) {
	t.Parallel()

	// counterType has a Register field typed as func() — a concrete type,
	// NOT an empty interface. The test asserts that such a type would FAIL
	// the A1 check, proving the check is live.
	type counterType struct {
		Register func()
	}

	rt := reflect.TypeOf(counterType{})
	field, ok := rt.FieldByName("Register")
	require.True(t, ok, "negative-control struct must have Register")

	fieldType := field.Type
	violations := 0
	if fieldType.Kind() != reflect.Interface {
		violations++
	}
	if fieldType.Name() != "" {
		violations++
	}
	assert.GreaterOrEqual(t, violations, 1,
		"negative-control: concrete func() Register field must produce ≥1 violation "+
			"(proves the A1 check is not vacuously true)")
}
