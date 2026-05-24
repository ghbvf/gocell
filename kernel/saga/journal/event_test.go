package journal_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/saga/journal"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/idutil"
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
		{journal.KindSagaTerminal, true},
		{journal.EventKind(0), false},
		{journal.EventKind(6), false},
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
		{journal.KindSagaTerminal, "saga_terminal"},
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

func TestEvent_ValidateForAppend_StepKinds(t *testing.T) {
	t.Parallel()

	stepKinds := []journal.EventKind{
		journal.KindStepStarted,
		journal.KindStepCompleted,
		journal.KindStepFailed,
		journal.KindStepCompensated,
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

func TestEvent_ValidateForAppend_TerminalRejected(t *testing.T) {
	t.Parallel()
	e := journal.Event{
		Kind:     journal.KindSagaTerminal,
		StepName: "",
		Payload:  nil,
	}
	err := e.ValidateForAppend()
	require.Error(t, err, "KindSagaTerminal must be rejected by ValidateForAppend")
	var ecErr *errcode.Error
	assert.True(t, errors.As(err, &ecErr))
}

func TestEvent_ValidateForAppend_InvalidKind(t *testing.T) {
	t.Parallel()
	tests := []journal.EventKind{
		journal.EventKind(0),
		journal.EventKind(6),
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
