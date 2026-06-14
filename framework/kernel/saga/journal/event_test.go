package journal_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/saga"
	"github.com/ghbvf/gocell/framework/kernel/saga/journal"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/idutil"
)

// validStep is a reusable SafeID that passes idutil validation — letters,
// digits and the allowed punctuation (._:/-).
var validStep = idutil.SafeID("reserve-inventory")

// invalidStep contains a space, which IsSafeID rejects.
var invalidStep = idutil.SafeID("bad id with spaces")

func TestEventKind_Valid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		k    journal.EventKind
		want bool
	}{
		{journal.KindStepStarted, true},
		{journal.KindStepCompleted, true},
		{journal.KindStepFailed, true},
		{journal.KindStepCompensated, true},
		{journal.KindCompensationStarted, true},
		{journal.KindSagaSucceeded, true},
		{journal.KindSagaFailed, true},
		{journal.KindSagaCompensated, true},
		{journal.KindSagaExpired, true},
		{journal.KindStepCompensationFailed, true},
		{journal.KindSagaCompensationFailed, true},
		{journal.EventKind(0), false},
		{journal.EventKind(12), false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.k.String(), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.k.Valid())
		})
	}
}

func TestEventKind_String(t *testing.T) {
	t.Parallel()
	tests := []struct {
		k    journal.EventKind
		want string
	}{
		{journal.KindStepStarted, "step_started"},
		{journal.KindStepCompleted, "step_completed"},
		{journal.KindStepFailed, "step_failed"},
		{journal.KindStepCompensated, "step_compensated"},
		{journal.KindCompensationStarted, "compensation_started"},
		{journal.KindSagaSucceeded, "saga_succeeded"},
		{journal.KindSagaFailed, "saga_failed"},
		{journal.KindSagaCompensated, "saga_compensated"},
		{journal.KindSagaExpired, "saga_expired"},
		{journal.KindStepCompensationFailed, "step_compensation_failed"},
		{journal.KindSagaCompensationFailed, "saga_compensation_failed"},
		{journal.EventKind(0), "eventkind(0)"},
		{journal.EventKind(99), "eventkind(99)"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.k.String())
		})
	}
}

// TestEventKind_IsTerminal verifies that exactly the 5 KindSaga* terminal kinds
// return true and the 5 non-terminal kinds return false.
func TestEventKind_IsTerminal(t *testing.T) {
	t.Parallel()
	tests := []struct {
		k        journal.EventKind
		terminal bool
	}{
		{journal.KindStepStarted, false},
		{journal.KindStepCompleted, false},
		{journal.KindStepFailed, false},
		{journal.KindStepCompensated, false},
		{journal.KindCompensationStarted, false},
		{journal.KindSagaSucceeded, true},
		{journal.KindSagaFailed, true},
		{journal.KindSagaCompensated, true},
		{journal.KindSagaExpired, true},
		{journal.KindStepCompensationFailed, false},
		{journal.KindSagaCompensationFailed, true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.k.String(), func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.terminal, tt.k.IsTerminal())
		})
	}
}

// TestTerminalEventKind verifies the TerminalEventKind mapping between terminal
// saga.Status values and their corresponding EventKind.
func TestTerminalEventKind(t *testing.T) {
	t.Parallel()

	// Each terminal status must map to exactly one distinct terminal EventKind.
	terminalCases := []struct {
		status   saga.Status
		wantKind journal.EventKind
	}{
		{saga.StatusSucceeded, journal.KindSagaSucceeded},
		{saga.StatusFailed, journal.KindSagaFailed},
		{saga.StatusCompensated, journal.KindSagaCompensated},
		{saga.StatusExpired, journal.KindSagaExpired},
		{saga.StatusCompensationFailed, journal.KindSagaCompensationFailed},
	}
	for _, tc := range terminalCases {
		tc := tc
		t.Run(tc.status.String(), func(t *testing.T) {
			t.Parallel()
			got, ok := journal.TerminalEventKind(tc.status)
			require.True(t, ok, "TerminalEventKind(%s) must return ok=true", tc.status)
			assert.Equal(t, tc.wantKind, got)
		})
	}

	// A non-terminal status must return ok=false.
	t.Run("non-terminal/Running", func(t *testing.T) {
		t.Parallel()
		_, ok := journal.TerminalEventKind(saga.StatusRunning)
		assert.False(t, ok, "TerminalEventKind(Running) must return ok=false")
	})
}

func TestEvent_ValidateForAppend_StepKinds(t *testing.T) {
	t.Parallel()

	stepKinds := []journal.EventKind{
		journal.KindStepStarted,
		journal.KindStepCompleted,
		journal.KindStepFailed,
		journal.KindStepCompensated,
		journal.KindStepCompensationFailed,
	}

	payloads := []struct {
		name    string
		payload []byte
		wantErr bool
	}{
		{"nil", nil, false},
		{"empty", []byte{}, false},
		{"null", []byte("null"), false},
		{"empty-object", []byte("{}"), false},
		{"object-with-key", []byte(`{"k":"v"}`), false},
		{"whitespace-object", []byte(" {\n} "), false},
		{"array", []byte("[1,2]"), true},
		{"scalar-int", []byte("42"), true},
		{"scalar-string", []byte(`"s"`), true},
		{"malformed", []byte("{bad"), true},
	}

	for _, kind := range stepKinds {
		kind := kind
		for _, p := range payloads {
			p := p
			name := kind.String() + "/" + p.name
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				e := journal.Event{
					Kind:     kind,
					StepName: validStep,
					Payload:  p.payload,
				}
				err := e.ValidateForAppend()
				if p.wantErr {
					require.Error(t, err)
					var ecErr *errcode.Error
					assert.True(t, errors.As(err, &ecErr),
						"expected *errcode.Error, got %T: %v", err, err)
				} else {
					assert.NoError(t, err)
				}
			})
		}
	}
}

// TestEvent_ValidateForAppend_TerminalRejected verifies that all terminal kinds
// (currently 5) are rejected by ValidateForAppend (they must go through MarkTerminal).
func TestEvent_ValidateForAppend_TerminalRejected(t *testing.T) {
	t.Parallel()

	terminalKinds := []journal.EventKind{
		journal.KindSagaSucceeded,
		journal.KindSagaFailed,
		journal.KindSagaCompensated,
		journal.KindSagaExpired,
		journal.KindSagaCompensationFailed,
	}
	for _, k := range terminalKinds {
		k := k
		t.Run(k.String(), func(t *testing.T) {
			t.Parallel()
			e := journal.Event{
				Kind:     k,
				StepName: "",
				Payload:  nil,
			}
			err := e.ValidateForAppend()
			require.Error(t, err, "%s must be rejected by ValidateForAppend", k)
			var ecErr *errcode.Error
			assert.True(t, errors.As(err, &ecErr))
		})
	}
}

// TestEvent_ValidateForAppend_CompensationStarted_NoStepName verifies that
// KindCompensationStarted is allowed WITHOUT a StepName (it is saga-scoped, not
// step-scoped), and with valid object/null payloads.
func TestEvent_ValidateForAppend_CompensationStarted_NoStepName(t *testing.T) {
	t.Parallel()

	validPayloads := []struct {
		name    string
		payload []byte
	}{
		{"nil", nil},
		{"empty", []byte{}},
		{"null", []byte("null")},
		{"object", []byte(`{"reason":"step-2-failed"}`)},
	}
	for _, p := range validPayloads {
		p := p
		t.Run(p.name, func(t *testing.T) {
			t.Parallel()
			e := journal.Event{
				Kind:     journal.KindCompensationStarted,
				StepName: "", // intentionally empty
				Payload:  p.payload,
			}
			assert.NoError(t, e.ValidateForAppend(),
				"KindCompensationStarted with empty StepName should pass ValidateForAppend")
		})
	}
}

func TestEvent_ValidateForAppend_InvalidKind(t *testing.T) {
	t.Parallel()
	tests := []journal.EventKind{
		journal.EventKind(0),
		journal.EventKind(12),
	}
	for _, k := range tests {
		k := k
		t.Run(k.String(), func(t *testing.T) {
			t.Parallel()
			e := journal.Event{
				Kind:     k,
				StepName: validStep,
			}
			err := e.ValidateForAppend()
			require.Error(t, err)
			var ecErr *errcode.Error
			assert.True(t, errors.As(err, &ecErr))
		})
	}
}

func TestEvent_ValidateForAppend_StepNameRequired(t *testing.T) {
	t.Parallel()
	stepKinds := []journal.EventKind{
		journal.KindStepStarted,
		journal.KindStepCompleted,
		journal.KindStepFailed,
		journal.KindStepCompensated,
		journal.KindStepCompensationFailed,
	}
	for _, kind := range stepKinds {
		kind := kind
		t.Run("empty/"+kind.String(), func(t *testing.T) {
			t.Parallel()
			e := journal.Event{
				Kind:     kind,
				StepName: "",
			}
			err := e.ValidateForAppend()
			require.Error(t, err, "empty StepName must be rejected for %s", kind)
			var ecErr *errcode.Error
			assert.True(t, errors.As(err, &ecErr))
		})
		t.Run("invalid/"+kind.String(), func(t *testing.T) {
			t.Parallel()
			e := journal.Event{
				Kind:     kind,
				StepName: invalidStep,
			}
			err := e.ValidateForAppend()
			require.Error(t, err, "invalid StepName must be rejected for %s", kind)
			var ecErr *errcode.Error
			assert.True(t, errors.As(err, &ecErr))
		})
	}
}

// TestEvent_ValidateForAppend_PayloadAtAndOverMaxBytes locks the
// MaxPayloadBytes boundary contract introduced in PR-04 to close the
// unbounded-payload DoS vector before BYTEA storage.
//
//   - exactly MaxPayloadBytes: accepted (must be a valid JSON object, so we
//     pad an object literal up to the cap).
//   - MaxPayloadBytes + 1: rejected with KindInvalid / ErrValidationFailed.
func TestEvent_ValidateForAppend_PayloadAtAndOverMaxBytes(t *testing.T) {
	t.Parallel()
	// Build a JSON object whose serialized length is exactly MaxPayloadBytes.
	// Header `{"x":"` is 6 bytes, footer `"}` is 2 bytes, so pad with N=
	// MaxPayloadBytes-8 ASCII chars.
	const headerLen = 6
	const footerLen = 2
	padLen := journal.MaxPayloadBytes - headerLen - footerLen
	require.Positive(t, padLen)
	pad := make([]byte, padLen)
	for i := range pad {
		pad[i] = 'a'
	}
	atCap := append(append([]byte(`{"x":"`), pad...), []byte(`"}`)...)
	require.Len(t, atCap, journal.MaxPayloadBytes)

	at := journal.Event{Kind: journal.KindStepStarted, StepName: validStep, Payload: atCap}
	require.NoError(t, at.ValidateForAppend(), "payload exactly MaxPayloadBytes must be accepted")

	overCap := make([]byte, 0, len(atCap)+1)
	overCap = append(overCap, atCap...)
	overCap = append(overCap, 'a') // one byte over (size check fires before JSON parse)
	over := journal.Event{Kind: journal.KindStepStarted, StepName: validStep, Payload: overCap}
	err := over.ValidateForAppend()
	require.Error(t, err, "payload over MaxPayloadBytes must be rejected")
	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, errcode.KindInvalid, ecErr.Kind)
	assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code)
}

// Reference the saga package import (kept to avoid unused-import after
// editing this file in the future); the saga package powers other tests in
// this file's TerminalEventKind paths.
var _ = saga.StatusPending
