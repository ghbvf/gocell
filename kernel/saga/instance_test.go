package saga

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/idutil"
)

// testBase is the fixed reference instant for deterministic state-machine tests.
// Shared across instance_test.go and advance_test.go (same package).
var testBase = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func requireValidationError(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code)
}

func TestNewInstance_Defaults(t *testing.T) {
	t.Parallel()
	inst := NewInstance("inst-1", "def-1", testBase)
	assert.Equal(t, idutil.SafeID("inst-1"), inst.ID)
	assert.Equal(t, idutil.SafeID("def-1"), inst.DefinitionID)
	assert.Equal(t, StatusPending, inst.Status)
	assert.Equal(t, 0, inst.CurrentStep)
	assert.Equal(t, testBase, inst.StartedAt)
	assert.Nil(t, inst.UpdatedAt)
	assert.Nil(t, inst.CompletedAt)
	assert.NoError(t, inst.ValidateNew())
}

func TestInstance_ValidateNew(t *testing.T) {
	t.Parallel()
	ts := testBase
	tests := []struct {
		name    string
		mutate  func(*Instance)
		wantErr bool
	}{
		{"valid", func(*Instance) {}, false},
		{"empty ID", func(i *Instance) { i.ID = "" }, true},
		{"unsafe ID", func(i *Instance) { i.ID = "bad id!" }, true},
		{"empty DefinitionID", func(i *Instance) { i.DefinitionID = "" }, true},
		{"unsafe DefinitionID", func(i *Instance) { i.DefinitionID = "bad def!" }, true},
		{"non-pending status", func(i *Instance) { i.Status = StatusRunning }, true},
		{"invalid status", func(i *Instance) { i.Status = Status(0) }, true},
		{"nonzero CurrentStep", func(i *Instance) { i.CurrentStep = 1 }, true},
		{"zero StartedAt", func(i *Instance) { i.StartedAt = time.Time{} }, true},
		{"UpdatedAt set", func(i *Instance) { i.UpdatedAt = &ts }, true},
		{"CompletedAt set", func(i *Instance) { i.CompletedAt = &ts }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			inst := NewInstance("inst-1", "def-1", testBase)
			tt.mutate(&inst)
			err := inst.ValidateNew()
			if tt.wantErr {
				requireValidationError(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
