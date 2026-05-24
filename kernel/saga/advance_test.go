package saga

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// instanceInState builds an Instance advanced into the given non-terminal
// source state via the real state machine (no direct Status mutation).
func instanceInState(t *testing.T, s Status) Instance {
	t.Helper()
	inst := NewInstance("inst-1", "def-1", testBase)
	switch s {
	case StatusPending:
		// already pending
	case StatusRunning:
		require.NoError(t, AdvanceSaga(&inst, StatusRunning, testBase.Add(testtime.D1s)))
	case StatusCompensating:
		require.NoError(t, AdvanceSaga(&inst, StatusRunning, testBase.Add(testtime.D1s)))
		require.NoError(t, AdvanceSaga(&inst, StatusCompensating, testBase.Add(testtime.D2s)))
	default:
		t.Fatalf("instanceInState: cannot construct source state %s via state machine", s)
	}
	return inst
}

func TestAdvanceSaga_NilInstance(t *testing.T) {
	t.Parallel()
	requireValidationError(t, AdvanceSaga(nil, StatusRunning, testBase))
}

func TestAdvanceSaga_LegalEdges(t *testing.T) {
	t.Parallel()
	for _, e := range legalEdges {
		t.Run(e.from.String()+"->"+e.to.String(), func(t *testing.T) {
			t.Parallel()
			inst := instanceInState(t, e.from)
			now := testBase.Add(testtime.D10s)
			require.NoError(t, AdvanceSaga(&inst, e.to, now))
			assert.Equal(t, e.to, inst.Status)
			require.NotNil(t, inst.UpdatedAt)
			assert.Equal(t, now, *inst.UpdatedAt)
			if e.to.IsTerminal() {
				require.NotNil(t, inst.CompletedAt)
				assert.Equal(t, now, *inst.CompletedAt)
			} else {
				assert.Nil(t, inst.CompletedAt)
			}
		})
	}
}

func TestAdvanceSaga_IllegalEdgesFromNonTerminal(t *testing.T) {
	t.Parallel()
	for _, from := range []Status{StatusPending, StatusRunning, StatusCompensating} {
		for _, to := range allStatuses() {
			if isLegal(from, to) {
				continue
			}
			t.Run(from.String()+"->"+to.String(), func(t *testing.T) {
				t.Parallel()
				inst := instanceInState(t, from)
				err := AdvanceSaga(&inst, to, testBase.Add(testtime.D10s))
				requireValidationError(t, err)
				assert.Equal(t, from, inst.Status, "status must not change on illegal transition")
			})
		}
	}
}

func TestAdvanceSaga_FromTerminalAlwaysIllegal(t *testing.T) {
	t.Parallel()
	for _, from := range []Status{StatusSucceeded, StatusFailed, StatusCompensated, StatusExpired} {
		for _, to := range allStatuses() {
			t.Run(from.String()+"->"+to.String(), func(t *testing.T) {
				t.Parallel()
				inst := NewInstance("inst-1", "def-1", testBase)
				inst.Status = from // test-only direct set to reach a terminal source
				err := AdvanceSaga(&inst, to, testBase.Add(testtime.D10s))
				require.Error(t, err)
				assert.Equal(t, from, inst.Status)
			})
		}
	}
}

func TestAdvanceSaga_ClockRegressionRejected(t *testing.T) {
	t.Parallel()
	t.Run("now before StartedAt", func(t *testing.T) {
		t.Parallel()
		inst := NewInstance("inst-1", "def-1", testBase)
		err := AdvanceSaga(&inst, StatusRunning, testBase.Add(-testtime.D1s))
		requireValidationError(t, err)
		assert.Equal(t, StatusPending, inst.Status)
	})
	t.Run("now before previous UpdatedAt", func(t *testing.T) {
		t.Parallel()
		inst := instanceInState(t, StatusRunning) // UpdatedAt = testBase+1s
		err := AdvanceSaga(&inst, StatusSucceeded, testBase)
		requireValidationError(t, err)
		assert.Equal(t, StatusRunning, inst.Status)
	})
}

func TestAdvanceSaga_HappyLifecycle(t *testing.T) {
	t.Parallel()
	inst := NewInstance("inst-1", "def-1", testBase)
	require.NoError(t, AdvanceSaga(&inst, StatusRunning, testBase.Add(testtime.D1s)))
	require.NoError(t, AdvanceSaga(&inst, StatusSucceeded, testBase.Add(testtime.D2s)))
	assert.Equal(t, StatusSucceeded, inst.Status)
	assert.True(t, inst.Status.IsTerminal())
	require.NotNil(t, inst.CompletedAt)
	assert.Equal(t, testBase.Add(testtime.D2s), *inst.CompletedAt)
	assert.Error(t, AdvanceSaga(&inst, StatusFailed, testBase.Add(testtime.D3s)),
		"terminal state must reject further transitions")
}

func TestAdvanceSaga_CompensateLifecycle(t *testing.T) {
	t.Parallel()
	inst := NewInstance("inst-1", "def-1", testBase)
	require.NoError(t, AdvanceSaga(&inst, StatusRunning, testBase.Add(testtime.D1s)))
	require.NoError(t, AdvanceSaga(&inst, StatusCompensating, testBase.Add(testtime.D2s)))
	require.NoError(t, AdvanceSaga(&inst, StatusCompensated, testBase.Add(testtime.D3s)))
	assert.Equal(t, StatusCompensated, inst.Status)
	require.NotNil(t, inst.CompletedAt)
}

func TestAdvanceSaga_CompensatingToFailed(t *testing.T) {
	t.Parallel()
	inst := NewInstance("inst-1", "def-1", testBase)
	require.NoError(t, AdvanceSaga(&inst, StatusRunning, testBase.Add(testtime.D1s)))
	require.NoError(t, AdvanceSaga(&inst, StatusCompensating, testBase.Add(testtime.D2s)))
	require.NoError(t, AdvanceSaga(&inst, StatusFailed, testBase.Add(testtime.D3s)))
	assert.Equal(t, StatusFailed, inst.Status)
	require.NotNil(t, inst.CompletedAt)
}

func TestAdvanceStep(t *testing.T) {
	t.Parallel()
	t.Run("running increments cursor", func(t *testing.T) {
		t.Parallel()
		inst := instanceInState(t, StatusRunning)
		now := testBase.Add(testtime.D5s)
		require.NoError(t, AdvanceStep(&inst, 3, now))
		assert.Equal(t, 1, inst.CurrentStep)
		require.NotNil(t, inst.UpdatedAt)
		assert.Equal(t, now, *inst.UpdatedAt)
	})
	t.Run("rejects when not running", func(t *testing.T) {
		t.Parallel()
		inst := NewInstance("inst-1", "def-1", testBase) // Pending
		err := AdvanceStep(&inst, 3, testBase.Add(testtime.D1s))
		requireValidationError(t, err)
		assert.Equal(t, 0, inst.CurrentStep)
	})
	t.Run("rejects advancing past last step", func(t *testing.T) {
		t.Parallel()
		inst := instanceInState(t, StatusRunning) // CurrentStep 0
		err := AdvanceStep(&inst, 1, testBase.Add(testtime.D5s))
		requireValidationError(t, err)
		assert.Equal(t, 0, inst.CurrentStep)
	})
	t.Run("rejects clock regression", func(t *testing.T) {
		t.Parallel()
		inst := instanceInState(t, StatusRunning) // UpdatedAt = testBase+1s
		err := AdvanceStep(&inst, 3, testBase)
		requireValidationError(t, err)
		assert.Equal(t, 0, inst.CurrentStep)
	})
	t.Run("nil instance", func(t *testing.T) {
		t.Parallel()
		requireValidationError(t, AdvanceStep(nil, 3, testBase))
	})
}
