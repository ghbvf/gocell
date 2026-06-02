package cell

// registry_projection_test.go — record-only tests for Registrar.RegisterProjection.
//
// Coverage:
//   - happy path → snapshot.Projections carries the request (OnReset nil allowed)
//   - nil Apply / empty ProjectionID / empty CellID / non-event Kind / empty Topic
//     → fail-fast errcode naming the offending field
//   - Snapshot() returns an independent copy of the projections slice
//   - RegisterProjection after Snapshot() panics (mustNotBeFinalized)

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
)

func noopProjectionApply(_ context.Context, _ outbox.Entry) error { return nil }

func validProjectionRequest() ProjectionRequest {
	return ProjectionRequest{
		Spec:         testRegistrySpec("order-created"),
		ProjectionID: "ordersummary",
		CellID:       "ordercell",
		Apply:        noopProjectionApply,
	}
}

func TestRegisterProjection_HappyPath_RecordsRequest(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)
	req := validProjectionRequest()

	require.NoError(t, rec.RegisterProjection(req), "valid projection must record")

	snap := rec.Snapshot()
	require.Len(t, snap.Projections, 1, "snapshot must carry the projection")
	got := snap.Projections[0]
	assert.Equal(t, "ordersummary", got.ProjectionID)
	assert.Equal(t, "ordercell", got.CellID)
	assert.Equal(t, "order-created", got.Spec.Topic)
	require.NotNil(t, got.Apply, "Apply must survive into the snapshot")
	assert.Nil(t, got.OnReset, "nil OnReset is valid and preserved")
	// SliceID is optional — empty string is valid (bootstrap falls back to projectionID).
	assert.Equal(t, "", got.SliceID, "SliceID defaults to empty when not set")
}

func TestRegisterProjection_SliceID_RoundTrip(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)
	req := validProjectionRequest()
	req.SliceID = "myslice"

	require.NoError(t, rec.RegisterProjection(req))

	snap := rec.Snapshot()
	require.Len(t, snap.Projections, 1)
	assert.Equal(t, "myslice", snap.Projections[0].SliceID,
		"non-empty SliceID must survive into the snapshot")
}

func TestRegisterProjection_OnResetOptional_Preserved(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)
	req := validProjectionRequest()
	called := false
	req.OnReset = func(context.Context) error { called = true; return nil }

	require.NoError(t, rec.RegisterProjection(req))
	snap := rec.Snapshot()
	require.Len(t, snap.Projections, 1)
	require.NotNil(t, snap.Projections[0].OnReset, "OnReset must survive into the snapshot")
	require.NoError(t, snap.Projections[0].OnReset(context.Background()))
	assert.True(t, called, "recorded OnReset must be the one provided")
}

func TestRegisterProjection_Validation(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*ProjectionRequest)
		wantInMsg string
	}{
		{"nil apply", func(r *ProjectionRequest) { r.Apply = nil }, "Apply"},
		{"empty projectionID", func(r *ProjectionRequest) { r.ProjectionID = "" }, "ProjectionID"},
		{"empty cellID", func(r *ProjectionRequest) { r.CellID = "" }, "CellID"},
		{"non-event kind", func(r *ProjectionRequest) { r.Spec.Kind = "http" }, "event"},
		{"empty topic", func(r *ProjectionRequest) { r.Spec.Topic = "" }, "Topic"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)
			req := validProjectionRequest()
			tc.mutate(&req)

			err := rec.RegisterProjection(req)
			require.Error(t, err, "invalid request must error")
			var ecErr *errcode.Error
			require.ErrorAs(t, err, &ecErr, "must be *errcode.Error")
			assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code)
			assert.Contains(t, ecErr.Message, tc.wantInMsg, "message must name the offending field")

			assert.Empty(t, rec.Snapshot().Projections, "rejected request must not be recorded")
		})
	}
}

// TestRegisterProjection_DuplicateProjectionID_Rejected locks the
// single-projection-single-subscriber premise (#1369 F1). A projection's
// consumer group is derived as "<cellID>-<projectionID>" and its checkpoint key
// is (cellID, projectionID); registering the same projectionID twice would wire
// two competing subscribers onto one consumer group, which voids the serial
// in-order delivery that the projection checkpoint requires (the transport-level
// SerialInOrderGuarantor guard is scoped to a SINGLE subscriber per group — see
// runtime/eventbus.InMemoryEventBus.GuaranteesSerialInOrderDelivery godoc). The
// recorder is per-cell, so duplicate ProjectionID == duplicate within the cell.
func TestRegisterProjection_DuplicateProjectionID_Rejected(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)

	require.NoError(t, rec.RegisterProjection(validProjectionRequest()),
		"first projection must record")

	// Second registration with the same ProjectionID (even a different Spec/Topic)
	// must fail fast — the derived consumer group / checkpoint key would collide.
	dup := validProjectionRequest()
	dup.Spec = testRegistrySpec("order-updated") // different topic, same projectionID
	err := rec.RegisterProjection(dup)
	require.Error(t, err, "duplicate ProjectionID must be rejected")
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr, "must be *errcode.Error")
	assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code)
	assert.Contains(t, ecErr.Message, "duplicate", "message must name the duplicate condition")

	require.Len(t, rec.Snapshot().Projections, 1,
		"the rejected duplicate must not be recorded; only the first projection survives")
}

func TestRegisterProjection_SnapshotCopyIsIndependent(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)
	require.NoError(t, rec.RegisterProjection(validProjectionRequest()))

	snap := rec.Snapshot()
	require.Len(t, snap.Projections, 1)
	// White-box: Snapshot returns a defensive copy, so mutating the returned
	// slice element must NOT corrupt the recorder's internal slice (same
	// guarantee as Subscriptions/WebhookReceivers). This test is in-package, so
	// it can read rec.projections directly.
	snap.Projections[0].ProjectionID = "tampered"
	require.Len(t, rec.projections, 1)
	assert.Equal(t, "ordersummary", rec.projections[0].ProjectionID,
		"mutating the snapshot copy must not affect the recorder's internal slice")
}

func TestRegisterProjection_AfterSnapshotPanics(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)
	_ = rec.Snapshot()
	assert.Panics(t, func() {
		_ = rec.RegisterProjection(validProjectionRequest())
	}, "registration after Snapshot must panic")
}

// TestRegisterProjection_SpecValidate_RejectsInvalidSpec verifies that a
// ProjectionRequest whose Spec passes the Kind/Topic guards but fails
// ContractSpec.Validate() (e.g. empty Transport) is rejected before recording.
// This mirrors the symmetric guard added to Subscribe (F3).
func TestRegisterProjection_SpecValidate_RejectsInvalidSpec(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)
	req := validProjectionRequest()
	// Transport is required by Spec.Validate(); blank it out to trigger the error.
	req.Spec.Transport = ""

	err := rec.RegisterProjection(req)
	require.Error(t, err, "spec with empty Transport must be rejected")
	assert.Contains(t, err.Error(), "Transport", "error must reference the invalid field")
	assert.Empty(t, rec.Snapshot().Projections, "rejected request must not be recorded")
}

// Compile-time anchor: ProjectionRequest.Apply has the same underlying signature
// as projection.Apply so the bootstrap drain's named-type conversion is legal.
var _ ProjectionApply = (func(context.Context, outbox.Entry) error)(nil)
