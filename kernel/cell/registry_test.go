package cell

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/contractspec"
	"github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func noopHandler(_ context.Context, _ outbox.Entry) outbox.HandleResult {
	return outbox.Ack()
}

func testRegistrySpec(topic string) contractspec.ContractSpec {
	return contractspec.ContractSpec{
		ID:        "event." + topic + ".v1",
		Kind:      cellvocab.ContractEvent,
		Transport: "amqp",
		Topic:     topic,
	}
}

// ---------------------------------------------------------------------------
// TestRegistry_Config_ReturnsConstructorValue
// ---------------------------------------------------------------------------

func TestRegistry_Config_ReturnsConstructorValue(t *testing.T) {
	cfg := map[string]any{"port": 8080, "debug": true}
	rec := NewRegistryRecorder(cfg, outbox.DurabilityDurable)
	assert.Equal(t, cfg, rec.Config())
}

// ---------------------------------------------------------------------------
// TestRegistry_DurabilityMode_ReturnsConstructorValue
// ---------------------------------------------------------------------------

func TestRegistry_DurabilityMode_ReturnsConstructorValue(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDemo)
	assert.Equal(t, outbox.DurabilityDemo, rec.DurabilityMode())
}

// ---------------------------------------------------------------------------
// TestRegistry_Healthz_ReturnsWriteSink
// ---------------------------------------------------------------------------

// TestRegistry_Healthz_ReturnsWriteSink verifies Healthz() returns a usable
// write-side sink. The recorder is a pure accumulator and holds no live
// aggregator; its sink's Evaluate is a documented no-op.
func TestRegistry_Healthz_ReturnsWriteSink(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)
	sink := rec.Healthz()
	require.NotNil(t, sink)

	snap := sink.Evaluate(context.Background())
	assert.Equal(t, healthz.StatusUp, snap.Overall)
	assert.Empty(t, snap.Probes)
}

// ---------------------------------------------------------------------------
// TestRegistry_Healthz_RegisterProbe_AccumulatesInSnapshot
// ---------------------------------------------------------------------------

// TestRegistry_Healthz_RegisterProbe_AccumulatesInSnapshot verifies that probes
// registered via reg.Healthz().Register(...) appear in the RegistrySnapshot for
// the bootstrap layer to drain onto the runtime aggregator.
func TestRegistry_Healthz_RegisterProbe_AccumulatesInSnapshot(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)

	probe := healthz.NewProbe("foo_ready", func(_ context.Context) error { return nil })
	require.NoError(t, rec.Healthz().Register(probe))

	snap := rec.Snapshot()
	require.Len(t, snap.Probes, 1)
	assert.Equal(t, "foo_ready", snap.Probes[0].Name())
}

// ---------------------------------------------------------------------------
// TestRegistry_Healthz_DuplicateName_RejectsSecond
// ---------------------------------------------------------------------------

// TestRegistry_Healthz_DuplicateName_RejectsSecond verifies that registering a
// second probe with the same name returns ErrDuplicateProbe and does not store
// the duplicate (first-wins, same contract as the runtime aggregator).
func TestRegistry_Healthz_DuplicateName_RejectsSecond(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)

	p1 := healthz.NewProbe("my_probe", func(_ context.Context) error { return nil })
	p2 := healthz.NewProbe("my_probe", func(_ context.Context) error { return errors.New("fail") })

	require.NoError(t, rec.Healthz().Register(p1))
	err := rec.Healthz().Register(p2)
	require.Error(t, err)
	assert.True(t, errors.Is(err, healthz.ErrDuplicateProbe))

	require.Len(t, rec.Snapshot().Probes, 1)
}

// ---------------------------------------------------------------------------
// TestRegistry_Healthz_Deregister_RemovesProbe
// ---------------------------------------------------------------------------

// TestRegistry_Healthz_Deregister_RemovesProbe verifies the write-side sink's
// Deregister drops an accumulated probe (and frees the name for re-register).
func TestRegistry_Healthz_Deregister_RemovesProbe(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)
	sink := rec.Healthz()

	require.NoError(t, sink.Register(healthz.NewProbe("a_ready", func(_ context.Context) error { return nil })))
	require.NoError(t, sink.Register(healthz.NewProbe("b_ready", func(_ context.Context) error { return nil })))
	sink.Deregister("a_ready")
	sink.Deregister("missing_ready") // no-op on unknown name

	// Name freed: re-register under the deregistered name succeeds.
	require.NoError(t, sink.Register(healthz.NewProbe("a_ready", func(_ context.Context) error { return nil })))

	names := make([]string, 0, 2)
	for _, p := range rec.Snapshot().Probes {
		names = append(names, p.Name())
	}
	assert.ElementsMatch(t, []string{"b_ready", "a_ready"}, names)
}

// ---------------------------------------------------------------------------
// TestRegistry_Healthz_RegisterAfterSnapshot_Panics
// ---------------------------------------------------------------------------

// TestRegistry_Healthz_RegisterAfterSnapshot_Panics verifies probe registration
// after Snapshot() panics — same finalize guard as the other registration
// methods (no lazy registration past sealing).
func TestRegistry_Healthz_RegisterAfterSnapshot_Panics(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)
	_ = rec.Snapshot() // finalize
	assert.Panics(t, func() {
		_ = rec.Healthz().Register(healthz.NewProbe("late_ready", func(_ context.Context) error { return nil }))
	})
}

// emptyNameProbe is a malformed Probe whose Name() is empty. It cannot be built
// via healthz.NewProbe (which panics on an empty name), so this local type
// exercises the recorder sink's registration-time name validation.
type emptyNameProbe struct{}

func (emptyNameProbe) Name() string                  { return "" }
func (emptyNameProbe) Check(_ context.Context) error { return nil }

// TestRegistry_Healthz_RejectsNilAndEmptyName verifies the write-side sink
// rejects a nil probe and an empty-name probe with ErrInvalidProbeName (mirrors
// the runtime aggregator contract) and does not accumulate the rejected probe.
func TestRegistry_Healthz_RejectsNilAndEmptyName(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)
	sink := rec.Healthz()
	assert.ErrorIs(t, sink.Register(nil), healthz.ErrInvalidProbeName)
	assert.ErrorIs(t, sink.Register(emptyNameProbe{}), healthz.ErrInvalidProbeName)
	assert.Empty(t, rec.Snapshot().Probes, "rejected probes must not be accumulated")
}

// ---------------------------------------------------------------------------
// TestRegistry_RouteGroup_AccumulatesInOrder
// ---------------------------------------------------------------------------

func TestRegistry_RouteGroup_AccumulatesInOrder(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)

	g1 := SingleGroup(PrimaryListener, "/api/v1/a", func(RouteMux) error { return nil })
	g2 := SingleGroup(InternalListener, "/internal/v1/b", func(RouteMux) error { return nil })
	rec.RouteGroup(g1)
	rec.RouteGroup(g2)

	snap := rec.Snapshot()
	require.Len(t, snap.RouteGroups, 2)
	assert.Equal(t, "/api/v1/a", snap.RouteGroups[0].Prefix)
	assert.Equal(t, "/internal/v1/b", snap.RouteGroups[1].Prefix)
}

// ---------------------------------------------------------------------------
// TestRegistry_Subscribe_RejectsNilHandler
// ---------------------------------------------------------------------------

func TestRegistry_Subscribe_RejectsNilHandler(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)
	err := rec.Subscribe(testRegistrySpec("user.created"), nil, "cg-test", "ordercell")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "handler")
}

// ---------------------------------------------------------------------------
// TestRegistry_Subscribe_RejectsEmptyConsumerGroup
// ---------------------------------------------------------------------------

func TestRegistry_Subscribe_RejectsEmptyConsumerGroup(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)
	err := rec.Subscribe(testRegistrySpec("user.created"), noopHandler, "", "ordercell")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "consumerGroup")
}

// ---------------------------------------------------------------------------
// TestRegistry_Subscribe_RejectsEmptyCellID
// ---------------------------------------------------------------------------

// K#07 HARD contract: cellID is a positional, mandatory string. The validator
// rejects empty strings so a programmer passing "" (e.g. via a misconfigured
// cellgen template) sees a fail-fast error rather than a downstream silent
// failure in bootstrap drain.
func TestRegistry_Subscribe_RejectsEmptyCellID(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)
	err := rec.Subscribe(testRegistrySpec("user.created"), noopHandler, "cg-test", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cellID")
}

// ---------------------------------------------------------------------------
// TestRegistry_Subscribe_CellIDCheckedBeforeContractTriple
// ---------------------------------------------------------------------------

// TestRegistry_Subscribe_CellIDCheckedBeforeContractTriple verifies that
// Validate checks cellID before the contract triple (ContractID etc.).
// A request with empty cellID but otherwise valid spec must fail with an
// error mentioning "cellID" and must NOT mention "ContractID" — proving
// the ordering: cellID guard fires first.
func TestRegistry_Subscribe_CellIDCheckedBeforeContractTriple(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)
	spec := testRegistrySpec("order.placed") // valid Topic, Kind, ContractID
	err := rec.Subscribe(spec, noopHandler, "cg-order", "")
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "cellid",
		"error must mention cellID so caller knows which parameter is missing")
	assert.NotContains(t, strings.ToLower(err.Error()), "contractid",
		"ContractID check must not have fired before cellID check")
}

// ---------------------------------------------------------------------------
// TestRegistry_Subscribe_RejectsBadSpecKind
// ---------------------------------------------------------------------------

func TestRegistry_Subscribe_RejectsBadSpecKind(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)
	spec := contractspec.ContractSpec{
		ID:        "http.foo.v1",
		Kind:      cellvocab.ContractHTTP, // not event
		Transport: "amqp",
		Topic:     "foo",
	}
	err := rec.Subscribe(spec, noopHandler, "cg-test", "ordercell")
	require.Error(t, err)
	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Contains(t, strings.ToLower(ecErr.Message), "kind")
}

// ---------------------------------------------------------------------------
// TestRegistry_Subscribe_HappyPath_AppendsToSnapshot
// ---------------------------------------------------------------------------

func TestRegistry_Subscribe_HappyPath_AppendsToSnapshot(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)

	spec := testRegistrySpec("order.placed")
	err := rec.Subscribe(spec, noopHandler, "cg-order", "ordercell", WithSubscriptionSliceID("order-slice"))
	require.NoError(t, err)

	snap := rec.Snapshot()
	require.Len(t, snap.Subscriptions, 1)
	assert.Equal(t, spec, snap.Subscriptions[0].Spec)
	assert.Equal(t, "cg-order", snap.Subscriptions[0].ConsumerGroup)
	assert.Equal(t, "ordercell", snap.Subscriptions[0].CellID)
	assert.Equal(t, "order-slice", snap.Subscriptions[0].SliceID)
	assert.NotNil(t, snap.Subscriptions[0].Handler)
}

// ---------------------------------------------------------------------------
// TestRegistry_Lifecycle_EmptyName_Panics
// ---------------------------------------------------------------------------

func TestRegistry_Lifecycle_EmptyName_Panics(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)
	assert.Panics(t, func() {
		rec.Lifecycle(LifecycleHook{Name: "", OnStart: func(context.Context) error { return nil }})
	})
}

// ---------------------------------------------------------------------------
// TestRegistry_Lifecycle_AccumulatesInOrder
// ---------------------------------------------------------------------------

func TestRegistry_Lifecycle_AccumulatesInOrder(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)

	rec.Lifecycle(LifecycleHook{Name: "alpha", OnStart: func(context.Context) error { return nil }})
	rec.Lifecycle(LifecycleHook{Name: "beta", OnStop: func(context.Context) error { return nil }})

	snap := rec.Snapshot()
	require.Len(t, snap.LifecycleHooks, 2)
	assert.Equal(t, "alpha", snap.LifecycleHooks[0].Name)
	assert.Equal(t, "beta", snap.LifecycleHooks[1].Name)
}

// ---------------------------------------------------------------------------
// TestRegistry_OnConfigReload_PrefixesRecorded
// ---------------------------------------------------------------------------

func TestRegistry_OnConfigReload_PrefixesRecorded(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)

	fn1 := func(_ context.Context, _ ConfigChangeEvent) error { return nil }
	fn2 := func(_ context.Context, _ ConfigChangeEvent) error { return nil }
	rec.OnConfigReload([]string{"auth."}, fn1)
	rec.OnConfigReload(nil, fn2) // nil = all keys

	snap := rec.Snapshot()
	require.Len(t, snap.ConfigReloaders, 2)
	assert.Equal(t, []string{"auth."}, snap.ConfigReloaders[0].Prefixes)
	assert.Nil(t, snap.ConfigReloaders[1].Prefixes)
	assert.NotNil(t, snap.ConfigReloaders[0].Fn)
	assert.NotNil(t, snap.ConfigReloaders[1].Fn)
}

// ---------------------------------------------------------------------------
// TestRegistry_OnConfigReload_EmptyPrefixPanics
// ---------------------------------------------------------------------------

func TestRegistry_OnConfigReload_EmptyPrefixPanics(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)
	fn := func(_ context.Context, _ ConfigChangeEvent) error { return nil }
	assert.Panics(t, func() {
		rec.OnConfigReload([]string{"valid.", ""}, fn) // empty string in slice → panic
	})
}

// ---------------------------------------------------------------------------
// TestRegistry_PostSnapshot_RouteGroup_Panics
// ---------------------------------------------------------------------------

func TestRegistry_PostSnapshot_RouteGroup_Panics(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)
	_ = rec.Snapshot() // finalize
	assert.Panics(t, func() {
		rec.RouteGroup(SingleGroup(PrimaryListener, "/", func(RouteMux) error { return nil }))
	})
}

// ---------------------------------------------------------------------------
// TestRegistry_PostSnapshot_Subscribe_Panics
// ---------------------------------------------------------------------------

func TestRegistry_PostSnapshot_Subscribe_Panics(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)
	_ = rec.Snapshot() // finalize
	assert.Panics(t, func() {
		_ = rec.Subscribe(testRegistrySpec("x"), noopHandler, "cg", "ordercell")
	})
}

// ---------------------------------------------------------------------------
// TestRegistry_PostSnapshot_Lifecycle_Panics
// ---------------------------------------------------------------------------

func TestRegistry_PostSnapshot_Lifecycle_Panics(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)
	_ = rec.Snapshot() // finalize
	assert.Panics(t, func() {
		rec.Lifecycle(LifecycleHook{Name: "x"})
	})
}

// ---------------------------------------------------------------------------
// TestRegistry_PostSnapshot_OnConfigReload_Panics
// ---------------------------------------------------------------------------

func TestRegistry_PostSnapshot_OnConfigReload_Panics(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)
	_ = rec.Snapshot()
	assert.Panics(t, func() {
		rec.OnConfigReload(nil, func(_ context.Context, _ ConfigChangeEvent) error { return nil })
	})
}

// ---------------------------------------------------------------------------
// RouteGroup struct tests (absorbed from routegroup_test.go)
// ---------------------------------------------------------------------------

func TestRouteGroupStruct_ZeroValue(t *testing.T) {
	var rg RouteGroup
	assert.True(t, rg.Listener.IsZero())
	assert.Nil(t, rg.Middleware)
	assert.Nil(t, rg.Register)
}

func TestRouteGroupStruct_SingleGroupConstructor(t *testing.T) {
	rg := SingleGroup(PrimaryListener, "/api/v1/sg", func(RouteMux) error { return nil })
	assert.Equal(t, "primary", rg.Listener.String())
	assert.Equal(t, "/api/v1/sg", rg.Prefix)
	assert.NotNil(t, rg.Register)
}

func TestRouteGroupStruct_FieldCombinations(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		rg      RouteGroup
		wantRef string
		wantPfx string
		nilReg  bool
	}{
		{
			name:    "primary_listener",
			rg:      RouteGroup{Listener: PrimaryListener, Prefix: "/api/v1/x", Register: func(RouteMux) error { return nil }},
			wantRef: "primary", wantPfx: "/api/v1/x", nilReg: false,
		},
		{
			name:    "internal_listener",
			rg:      RouteGroup{Listener: InternalListener, Prefix: "/internal/v1/y", Register: func(RouteMux) error { return nil }},
			wantRef: "internal", wantPfx: "/internal/v1/y", nilReg: false,
		},
		{
			name:    "health_listener_empty_prefix",
			rg:      RouteGroup{Listener: HealthListener, Prefix: "", Register: func(RouteMux) error { return nil }},
			wantRef: "health", wantPfx: "", nilReg: false,
		},
		{
			name:    "nil_register",
			rg:      RouteGroup{Listener: PrimaryListener, Prefix: "/api/v1/z"},
			wantRef: "primary", wantPfx: "/api/v1/z", nilReg: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.wantRef, tc.rg.Listener.String())
			assert.Equal(t, tc.wantPfx, tc.rg.Prefix)
			assert.Equal(t, tc.nilReg, tc.rg.Register == nil)
		})
	}
}

// ---------------------------------------------------------------------------
// TestRegistry_Subscribe_RejectsEmptyTopic
// ---------------------------------------------------------------------------

func TestRegistry_Subscribe_RejectsEmptyTopic(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)
	spec := contractspec.ContractSpec{
		ID:        "event.foo.v1",
		Kind:      cellvocab.ContractEvent,
		Transport: "amqp",
		Topic:     "", // empty — must be rejected
	}
	err := rec.Subscribe(spec, noopHandler, "cg-test", "ordercell")
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "topic")
}

// ---------------------------------------------------------------------------
// TestRegistry_OnConfigReload_NilFn_Panics
// ---------------------------------------------------------------------------

func TestRegistry_OnConfigReload_NilFn_Panics(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)
	require.Panics(t, func() {
		rec.OnConfigReload([]string{"cfg."}, nil)
	})
}

// ---------------------------------------------------------------------------
// TestRegistry_Snapshot_DefensiveCopy
// ---------------------------------------------------------------------------

func TestRegistry_Snapshot_DefensiveCopy(t *testing.T) {
	// Verify that the RegistrySnapshot is a defensive copy: mutating the
	// snapshot's slices must not affect the recorder's internal state.
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)

	// Register one of each accumulated type before taking the snapshot.
	rec.RouteGroup(SingleGroup(PrimaryListener, "/api/v1/x", func(RouteMux) error { return nil }))
	err := rec.Subscribe(testRegistrySpec("snap.test"), noopHandler, "cg-snap", "ordercell")
	require.NoError(t, err)

	snap := rec.Snapshot()

	// Mutate snap fields.
	snap.RouteGroups = append(snap.RouteGroups, SingleGroup(PrimaryListener, "/extra", func(RouteMux) error { return nil }))
	snap.Subscriptions = append(snap.Subscriptions, SubscriptionRequest{ConsumerGroup: "injected"})

	// The recorder's internal counters must remain at the original sizes.
	assert.Len(t, rec.routeGroups, 1, "snap mutation must not affect recorder.routeGroups")
	assert.Len(t, rec.subscriptions, 1, "snap mutation must not affect recorder.subscriptions")
}

// ---------------------------------------------------------------------------
// TestRegistry_WithLogger_UsesCustomLogger
// ---------------------------------------------------------------------------

// TestRegistry_WithLogger_UsesCustomLogger verifies NewRegistryRecorderWithLogger
// wires up the custom logger and returns a functioning recorder.
func TestRegistry_WithLogger_UsesCustomLogger(t *testing.T) {
	var logBuf strings.Builder
	handler := slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})
	rec := NewRegistryRecorderWithLogger(nil, outbox.DurabilityDurable, slog.New(handler))

	// Recorder must be usable after construction.
	assert.NotNil(t, rec)
	require.NotNil(t, rec.Healthz())
}

// Compile-time: RouteGroup.Register accepts RouteMux.
var _ = RouteGroup{Register: func(m RouteMux) error {
	m.Handle("GET /", http.NotFoundHandler())
	return nil
}}
