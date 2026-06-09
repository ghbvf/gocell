package command

import (
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/errcode/errcodetest"
)

// fakeHandler is a local stand-in for the handler types that generated
// command_gen.go code will produce. Proves that the boxed-as-any round-trip
// works correctly.
type fakeHandler struct{ called bool }

func (h *fakeHandler) Handle() { h.called = true }

// fakeHandlerContract is the interface a generated Dispatch function would
// type-assert to after LookupHandler, proving the generated-code pattern works.
type fakeHandlerContract interface {
	Handle()
}

// validCommandID is a well-formed CommandID (idutil.SafeID) used across tests.
const validCommandID CommandID = "command.device-command.enqueue.v1"

// TestRegistry_RegisterThenLookup verifies the basic register→lookup round-trip.
func TestRegistry_RegisterThenLookup(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	h := &fakeHandler{}

	err := r.RegisterHandler(validCommandID, h)
	require.NoError(t, err)

	got, ok := r.LookupHandler(validCommandID)
	require.True(t, ok, "LookupHandler must return ok=true for a registered id")
	require.NotNil(t, got)
	assert.Equal(t, h, got)
}

// TestRegistry_TypeAssertBack proves that the boxed-as-any storage supports the
// generated Dispatch pattern: box a concrete *fakeHandler, register, lookup,
// type-assert back to fakeHandlerContract, invoke a method.
func TestRegistry_TypeAssertBack(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	h := &fakeHandler{}

	require.NoError(t, r.RegisterHandler(validCommandID, h))

	got, ok := r.LookupHandler(validCommandID)
	require.True(t, ok)

	// Simulate what generated Dispatch code does.
	typed, ok := got.(fakeHandlerContract)
	require.True(t, ok, "type-assert from boxed any to handler interface must succeed")
	typed.Handle()
	assert.True(t, h.called, "Handle() invoked via type-asserted interface must reach concrete handler")
}

// TestRegistry_DuplicateRegistration verifies that registering the same id twice
// returns a KindConflict / ErrConflict error.
func TestRegistry_DuplicateRegistration(t *testing.T) {
	t.Parallel()
	r := NewRegistry()
	h1 := &fakeHandler{}
	h2 := &fakeHandler{}

	require.NoError(t, r.RegisterHandler(validCommandID, h1))

	err := r.RegisterHandler(validCommandID, h2)
	require.Error(t, err, "second registration of the same id must error")
	errcodetest.AssertCode(t, err, errcode.ErrConflict)

	// Also verify the Kind.
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.KindConflict, ec.Kind)
}

// TestRegistry_UnknownID verifies that LookupHandler returns (nil, false) for an
// id that was never registered.
func TestRegistry_UnknownID(t *testing.T) {
	t.Parallel()
	r := NewRegistry()

	got, ok := r.LookupHandler("command.nonexistent.v1")
	assert.False(t, ok, "LookupHandler must return ok=false for unknown id")
	assert.Nil(t, got, "LookupHandler must return nil handler for unknown id")
}

// TestRegistry_NilHandler verifies that a bare nil handler is rejected with
// KindInvalid.
func TestRegistry_NilHandler(t *testing.T) {
	t.Parallel()
	r := NewRegistry()

	err := r.RegisterHandler(validCommandID, nil)
	require.Error(t, err, "nil handler must be rejected")
	errcodetest.AssertCode(t, err, errcode.ErrValidationFailed)

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec))
	assert.Equal(t, errcode.KindInvalid, ec.Kind)
}

// TestRegistry_TypedNilHandler verifies that a typed-nil handler (*fakeHandler
// typed as any carrying a nil pointer) is also rejected — IsNilInterface detects
// it.
func TestRegistry_TypedNilHandler(t *testing.T) {
	t.Parallel()
	r := NewRegistry()

	var typedNil *fakeHandler // typed nil — not caught by == nil
	err := r.RegisterHandler(validCommandID, typedNil)
	require.Error(t, err, "typed-nil handler must be rejected")
	errcodetest.AssertCode(t, err, errcode.ErrValidationFailed)
}

// TestRegistry_EmptyID verifies that an empty CommandID is rejected.
func TestRegistry_EmptyID(t *testing.T) {
	t.Parallel()
	r := NewRegistry()

	err := r.RegisterHandler("", &fakeHandler{})
	require.Error(t, err, "empty CommandID must be rejected")
	errcodetest.AssertCode(t, err, errcode.ErrValidationFailed)
}

// TestRegistry_MalformedSafeID verifies that a CommandID with unsafe characters
// is rejected by idutil.SafeID.Validate().
func TestRegistry_MalformedSafeID(t *testing.T) {
	t.Parallel()
	r := NewRegistry()

	// SafeID disallows spaces and special characters outside its allowed set.
	malformed := CommandID("command with spaces!!!")
	err := r.RegisterHandler(malformed, &fakeHandler{})
	require.Error(t, err, "malformed CommandID must be rejected")
	errcodetest.AssertCode(t, err, errcode.ErrValidationFailed)
}

// TestRegistry_SafeIDValidateGateway verifies that any CommandID that fails
// idutil.SafeID.Validate() is rejected. Uses a table-driven approach.
func TestRegistry_SafeIDValidateGateway(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		id   CommandID
	}{
		{"empty", ""},
		{"space in id", "has space"},
		{"unicode", "αβγ"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := NewRegistry()
			err := r.RegisterHandler(tc.id, &fakeHandler{})
			require.Error(t, err)
			errcodetest.AssertCode(t, err, errcode.ErrValidationFailed)
		})
	}
}

// TestRegistry_Concurrency exercises concurrent RegisterHandler + LookupHandler
// calls for race-detector coverage. Uses distinct ids so all registrations
// succeed (no deliberate conflict), while concurrent readers look up both known
// and unknown ids.
func TestRegistry_Concurrency(t *testing.T) {
	t.Parallel()
	r := NewRegistry()

	const n = 50
	var wg sync.WaitGroup

	// Writers: register n distinct command ids concurrently.
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := CommandID("command.concurrent-" + string(rune('a'+i%26)) + "-cmd.v1")
			// Ignore errors — some goroutines may register the same letter-bucket
			// twice (i%26 collision), which is expected to return ErrConflict.
			_ = r.RegisterHandler(id, &fakeHandler{})
		}(i)
	}

	// Readers: concurrent lookups of an id that may or may not exist yet.
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = r.LookupHandler(validCommandID)
			_, _ = r.LookupHandler("command.nonexistent.v999")
		}()
	}

	wg.Wait()
}
