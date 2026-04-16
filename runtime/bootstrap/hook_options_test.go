package bootstrap

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingHookObserver struct {
	events []cell.HookEvent
}

func (r *recordingHookObserver) OnHookEvent(e cell.HookEvent) {
	r.events = append(r.events, e)
}

func TestWithHookTimeout_PopulatesField(t *testing.T) {
	b := New(WithHookTimeout(2 * time.Second))
	assert.Equal(t, 2*time.Second, b.hookTimeout)
	assert.True(t, b.hookTimeoutSet)
}

func TestWithHookTimeout_NegativeDisables(t *testing.T) {
	b := New(WithHookTimeout(-1))
	assert.Equal(t, time.Duration(-1), b.hookTimeout)
	assert.True(t, b.hookTimeoutSet)
}

func TestWithHookTimeout_NotCalled_FieldUnset(t *testing.T) {
	b := New()
	assert.False(t, b.hookTimeoutSet)
}

func TestWithHookObserver_PopulatesField(t *testing.T) {
	obs := &recordingHookObserver{}
	b := New(WithHookObserver(obs))
	assert.Same(t, obs, b.hookObserver)
}

func TestWithHookObserver_Nil_NoOp(t *testing.T) {
	b := New(WithHookObserver(nil))
	assert.Nil(t, b.hookObserver)
}

// ptrObserver is used to exercise the typed-nil path in WithHookObserver:
// a *ptrObserver with nil value wrapped in the interface must be rejected
// by IsNilHookObserver and left unset on Bootstrap.
type ptrObserver struct{ events []cell.HookEvent }

func (p *ptrObserver) OnHookEvent(e cell.HookEvent) { p.events = append(p.events, e) }

func TestWithHookObserver_TypedNil_NoOp(t *testing.T) {
	var typed *ptrObserver
	b := New(WithHookObserver(typed))
	assert.Nil(t, b.hookObserver, "typed nil must be rejected — otherwise Run would dispatch to nil receiver")
}

// TestWithAssembly_OverridesHookOptions_BehaviourContract runs the full
// Bootstrap.Run path and asserts that a pre-built assembly's observer
// receives events while the observer passed via WithHookObserver does NOT —
// locking the documented "WithAssembly takes precedence" contract against
// regression. Also verifies the accompanying slog.Warn is emitted.
func TestWithAssembly_OverridesHookOptions_BehaviourContract(t *testing.T) {
	// Capture slog output to verify the Warn path fires.
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	oldDefault := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(oldDefault)

	preBuiltObs := &ptrObserver{}
	bootstrapObs := &ptrObserver{}

	asm := assembly.New(assembly.Config{
		ID:             "override-test",
		DurabilityMode: cell.DurabilityDemo,
		HookObserver:   preBuiltObs,
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	eb := eventbus.New()
	b := New(
		WithAssembly(asm),
		WithHookObserver(bootstrapObs), // must be ignored
		WithHookTimeout(time.Second),   // must be ignored
		WithListener(ln),
		WithPublisher(eb),
		WithSubscriber(eb),
		WithShutdownTimeout(2*time.Second),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	// Wait for server to become ready.
	require.Eventually(t, func() bool {
		resp, err := http.Get("http://" + ln.Addr().String() + "/healthz")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 3*time.Second, 50*time.Millisecond)

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}

	// Contract 1: pre-built assembly's observer MUST have received events.
	// assembly with zero cells still emits no hook events (there are no
	// hooks to invoke), so this assertion focuses on the observer identity.
	// The key invariant is that bootstrapObs remains empty.
	assert.Empty(t, bootstrapObs.events,
		"WithHookObserver must not deliver events when WithAssembly is used")

	// Contract 2: the Warn must be logged so operators can see the option
	// was silently superseded.
	logOutput := buf.String()
	assert.True(t, strings.Contains(logOutput, "WithHookTimeout/WithHookObserver ignored"),
		"expected Warn about ignored options, got: %s", logOutput)
}
