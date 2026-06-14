package cell_test

// grpc_service_test.go — TDD coverage for GRPCServiceSpec + GRPCService recorder
// method (GAP-1 PR-7 [#1150]).
//
// Cases:
//   1. GRPCServiceSpec.Validate() accepts valid spec
//   2. GRPCServiceSpec.Validate() rejects missing ContractID / CellID / Listener / Register
//   3. RegistryRecorder.GRPCService: happy path accumulates spec
//   4. RegistryRecorder.GRPCService: duplicate ContractID returns error
//   5. RegistryRecorder.GRPCService: validate error propagates
//   6. RegistryRecorder.GRPCService: called after Snapshot() panics
//
// The defensive-copy invariant (snapshot must not alias the recorder's internal
// slice) needs to observe the recorder's unexported grpcServices field, so it
// lives in the in-package test registry_test.go
// (TestRegistrySnapshot_GRPCServices_DefensiveCopy) rather than here.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
)

// validGRPCSpec returns a well-formed GRPCServiceSpec for testing.
func validGRPCSpec(contractID string) cell.GRPCServiceSpec {
	return cell.GRPCServiceSpec{
		ContractID: contractID,
		CellID:     "test-cell",
		Listener:   cell.PrimaryListener,
		Register:   func() {}, // non-nil any; type check happens in runtime/grpc layer
	}
}

// TestGRPCServiceSpec_Validate_HappyPath verifies a complete spec passes Validate.
func TestGRPCServiceSpec_Validate_HappyPath(t *testing.T) {
	t.Parallel()
	spec := validGRPCSpec("grpc.my.service.v1")
	require.NoError(t, spec.Validate())
}

// TestGRPCServiceSpec_Validate_MissingFields verifies each required field is checked.
func TestGRPCServiceSpec_Validate_MissingFields(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		mutate  func(*cell.GRPCServiceSpec)
		wantErr string
	}{
		{
			name:    "missing ContractID",
			mutate:  func(s *cell.GRPCServiceSpec) { s.ContractID = "" },
			wantErr: "ContractID",
		},
		{
			name:    "missing CellID",
			mutate:  func(s *cell.GRPCServiceSpec) { s.CellID = "" },
			wantErr: "CellID",
		},
		{
			name:    "zero Listener",
			mutate:  func(s *cell.GRPCServiceSpec) { s.Listener = cell.ListenerRef{} },
			wantErr: "Listener",
		},
		{
			name:    "nil Register",
			mutate:  func(s *cell.GRPCServiceSpec) { s.Register = nil },
			wantErr: "Register",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			spec := validGRPCSpec("grpc.svc.v1")
			tc.mutate(&spec)
			err := spec.Validate()
			require.Error(t, err)
			assert.ErrorContains(t, err, tc.wantErr)
		})
	}
}

// TestRegistryRecorder_GRPCService_HappyPath verifies a valid spec is accumulated.
func TestRegistryRecorder_GRPCService_HappyPath(t *testing.T) {
	t.Parallel()
	r := cell.NewRegistryRecorder(nil, outbox.DurabilityDemo)
	spec := validGRPCSpec("grpc.my.service.v1")
	require.NoError(t, r.GRPCService(spec))

	snap := r.Snapshot()
	require.Len(t, snap.GRPCServices, 1)
	assert.Equal(t, "grpc.my.service.v1", snap.GRPCServices[0].ContractID)
}

// TestRegistryRecorder_GRPCService_DuplicateContractID verifies that registering
// the same ContractID twice returns a non-nil error.
func TestRegistryRecorder_GRPCService_DuplicateContractID(t *testing.T) {
	t.Parallel()
	r := cell.NewRegistryRecorder(nil, outbox.DurabilityDemo)
	spec := validGRPCSpec("grpc.my.service.v1")
	require.NoError(t, r.GRPCService(spec))
	err := r.GRPCService(spec)
	require.Error(t, err)
	assert.ErrorContains(t, err, "duplicate")
}

// TestRegistryRecorder_GRPCService_ValidateError verifies validation errors propagate.
func TestRegistryRecorder_GRPCService_ValidateError(t *testing.T) {
	t.Parallel()
	r := cell.NewRegistryRecorder(nil, outbox.DurabilityDemo)
	spec := validGRPCSpec("") // empty ContractID → Validate fails
	err := r.GRPCService(spec)
	require.Error(t, err)
}

// TestRegistryRecorder_GRPCService_AfterSnapshot_Panics verifies that calling
// GRPCService after Snapshot() has been called panics with the registry-finalized panic.
func TestRegistryRecorder_GRPCService_AfterSnapshot_Panics(t *testing.T) {
	t.Parallel()
	r := cell.NewRegistryRecorder(nil, outbox.DurabilityDemo)
	_ = r.Snapshot() // finalize
	require.Panics(t, func() {
		_ = r.GRPCService(validGRPCSpec("grpc.svc.v1"))
	})
}
