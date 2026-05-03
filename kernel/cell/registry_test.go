package cell

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/wrapper"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func noopHandler(_ context.Context, _ outbox.Entry) outbox.HandleResult {
	return outbox.HandleResult{Disposition: outbox.DispositionAck}
}

func testRegistrySpec(topic string) wrapper.ContractSpec {
	return wrapper.ContractSpec{
		ID:        "event." + topic + ".v1",
		Kind:      "event",
		Transport: "amqp",
		Topic:     topic,
	}
}

// ---------------------------------------------------------------------------
// TestRegistry_Config_ReturnsConstructorValue
// ---------------------------------------------------------------------------

func TestRegistry_Config_ReturnsConstructorValue(t *testing.T) {
	cfg := map[string]any{"port": 8080, "debug": true}
	rec := NewRegistryRecorder(cfg, DurabilityDurable)
	assert.Equal(t, cfg, rec.Config())
}

// ---------------------------------------------------------------------------
// TestRegistry_DurabilityMode_ReturnsConstructorValue
// ---------------------------------------------------------------------------

func TestRegistry_DurabilityMode_ReturnsConstructorValue(t *testing.T) {
	rec := NewRegistryRecorder(nil, DurabilityDemo)
	assert.Equal(t, DurabilityDemo, rec.DurabilityMode())
}

// ---------------------------------------------------------------------------
// TestRegistry_RouteGroup_AccumulatesInOrder
// ---------------------------------------------------------------------------

func TestRegistry_RouteGroup_AccumulatesInOrder(t *testing.T) {
	rec := NewRegistryRecorder(nil, DurabilityDurable)

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
	rec := NewRegistryRecorder(nil, DurabilityDurable)
	err := rec.Subscribe(testRegistrySpec("user.created"), nil, "cg-test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "handler")
}

// ---------------------------------------------------------------------------
// TestRegistry_Subscribe_RejectsEmptyConsumerGroup
// ---------------------------------------------------------------------------

func TestRegistry_Subscribe_RejectsEmptyConsumerGroup(t *testing.T) {
	rec := NewRegistryRecorder(nil, DurabilityDurable)
	err := rec.Subscribe(testRegistrySpec("user.created"), noopHandler, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "consumerGroup")
}

// ---------------------------------------------------------------------------
// TestRegistry_Subscribe_RejectsBadSpecKind
// ---------------------------------------------------------------------------

func TestRegistry_Subscribe_RejectsBadSpecKind(t *testing.T) {
	rec := NewRegistryRecorder(nil, DurabilityDurable)
	spec := wrapper.ContractSpec{
		ID:        "http.foo.v1",
		Kind:      "http", // not "event"
		Transport: "amqp",
		Topic:     "foo",
	}
	err := rec.Subscribe(spec, noopHandler, "cg-test")
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "kind")
}

// ---------------------------------------------------------------------------
// TestRegistry_Subscribe_HappyPath_AppendsToSnapshot
// ---------------------------------------------------------------------------

func TestRegistry_Subscribe_HappyPath_AppendsToSnapshot(t *testing.T) {
	rec := NewRegistryRecorder(nil, DurabilityDurable)

	spec := testRegistrySpec("order.placed")
	err := rec.Subscribe(spec, noopHandler, "cg-order", WithSubscriptionSliceID("order-slice"))
	require.NoError(t, err)

	snap := rec.Snapshot()
	require.Len(t, snap.Subscriptions, 1)
	assert.Equal(t, spec, snap.Subscriptions[0].Spec)
	assert.Equal(t, "cg-order", snap.Subscriptions[0].ConsumerGroup)
	assert.Equal(t, "order-slice", snap.Subscriptions[0].SliceID)
	assert.NotNil(t, snap.Subscriptions[0].Handler)
}

// ---------------------------------------------------------------------------
// TestRegistry_Health_DuplicateName_LogsDropsSecond
// ---------------------------------------------------------------------------

func TestRegistry_Health_DuplicateName_LogsDropsSecond(t *testing.T) {
	// Redirect slog to a buffer so we can verify the error log.
	var logBuf strings.Builder
	handler := slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelError})
	rec := NewRegistryRecorderWithLogger(nil, DurabilityDurable, slog.New(handler))

	checker1 := func(_ context.Context) error { return nil }
	checker2 := func(_ context.Context) error { return assert.AnError }

	rec.Health("session-store", checker1)
	rec.Health("session-store", checker2) // duplicate — should log error + drop

	snap := rec.Snapshot()
	require.Contains(t, snap.HealthCheckers, "session-store")
	// checker1 must be retained (the second was dropped).
	assert.NoError(t, snap.HealthCheckers["session-store"](context.Background()))

	// A slog error must have been emitted.
	assert.Contains(t, logBuf.String(), "session-store")
}

// ---------------------------------------------------------------------------
// TestRegistry_Lifecycle_EmptyName_Panics
// ---------------------------------------------------------------------------

func TestRegistry_Lifecycle_EmptyName_Panics(t *testing.T) {
	rec := NewRegistryRecorder(nil, DurabilityDurable)
	assert.Panics(t, func() {
		rec.Lifecycle(LifecycleHook{Name: "", OnStart: func(context.Context) error { return nil }})
	})
}

// ---------------------------------------------------------------------------
// TestRegistry_Lifecycle_AccumulatesInOrder
// ---------------------------------------------------------------------------

func TestRegistry_Lifecycle_AccumulatesInOrder(t *testing.T) {
	rec := NewRegistryRecorder(nil, DurabilityDurable)

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
	rec := NewRegistryRecorder(nil, DurabilityDurable)

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
	rec := NewRegistryRecorder(nil, DurabilityDurable)
	fn := func(_ context.Context, _ ConfigChangeEvent) error { return nil }
	assert.Panics(t, func() {
		rec.OnConfigReload([]string{"valid.", ""}, fn) // empty string in slice → panic
	})
}

// ---------------------------------------------------------------------------
// TestRegistry_PostSnapshot_RouteGroup_Panics
// ---------------------------------------------------------------------------

func TestRegistry_PostSnapshot_RouteGroup_Panics(t *testing.T) {
	rec := NewRegistryRecorder(nil, DurabilityDurable)
	_ = rec.Snapshot() // finalize
	assert.Panics(t, func() {
		rec.RouteGroup(SingleGroup(PrimaryListener, "/", func(RouteMux) error { return nil }))
	})
}

// ---------------------------------------------------------------------------
// TestRegistry_PostSnapshot_Subscribe_Panics
// ---------------------------------------------------------------------------

func TestRegistry_PostSnapshot_Subscribe_Panics(t *testing.T) {
	rec := NewRegistryRecorder(nil, DurabilityDurable)
	_ = rec.Snapshot() // finalize
	assert.Panics(t, func() {
		_ = rec.Subscribe(testRegistrySpec("x"), noopHandler, "cg")
	})
}

// ---------------------------------------------------------------------------
// TestRegistry_PostSnapshot_Lifecycle_Panics
// ---------------------------------------------------------------------------

func TestRegistry_PostSnapshot_Lifecycle_Panics(t *testing.T) {
	rec := NewRegistryRecorder(nil, DurabilityDurable)
	_ = rec.Snapshot() // finalize
	assert.Panics(t, func() {
		rec.Lifecycle(LifecycleHook{Name: "x"})
	})
}

// ---------------------------------------------------------------------------
// TestRegistry_PostSnapshot_Health_Panics
// ---------------------------------------------------------------------------

func TestRegistry_PostSnapshot_Health_Panics(t *testing.T) {
	rec := NewRegistryRecorder(nil, DurabilityDurable)
	_ = rec.Snapshot()
	assert.Panics(t, func() {
		rec.Health("probe", func(context.Context) error { return nil })
	})
}

// ---------------------------------------------------------------------------
// TestRegistry_PostSnapshot_OnConfigReload_Panics
// ---------------------------------------------------------------------------

func TestRegistry_PostSnapshot_OnConfigReload_Panics(t *testing.T) {
	rec := NewRegistryRecorder(nil, DurabilityDurable)
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
	rec := NewRegistryRecorder(nil, DurabilityDurable)
	spec := wrapper.ContractSpec{
		ID:        "event.foo.v1",
		Kind:      "event",
		Transport: "amqp",
		Topic:     "", // empty — must be rejected
	}
	err := rec.Subscribe(spec, noopHandler, "cg-test")
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "topic")
}

// ---------------------------------------------------------------------------
// TestRegistry_OnConfigReload_NilFn_Panics
// ---------------------------------------------------------------------------

func TestRegistry_OnConfigReload_NilFn_Panics(t *testing.T) {
	rec := NewRegistryRecorder(nil, DurabilityDurable)
	require.Panics(t, func() {
		rec.OnConfigReload([]string{"cfg."}, nil)
	})
}

// ---------------------------------------------------------------------------
// TestRegistry_Health_EmptyName_Panics
// ---------------------------------------------------------------------------

func TestRegistry_Health_EmptyName_Panics(t *testing.T) {
	rec := NewRegistryRecorder(nil, DurabilityDurable)
	require.Panics(t, func() {
		rec.Health("", func(context.Context) error { return nil })
	})
}

// ---------------------------------------------------------------------------
// TestRegistry_Snapshot_DefensiveCopy
// ---------------------------------------------------------------------------

func TestRegistry_Snapshot_DefensiveCopy(t *testing.T) {
	// Verify that the RegistrySnapshot is a defensive copy: mutating the
	// snapshot's slices/maps must not affect the recorder's internal state.
	rec := NewRegistryRecorder(nil, DurabilityDurable)

	// Register one of each type before taking the snapshot.
	rec.RouteGroup(SingleGroup(PrimaryListener, "/api/v1/x", func(RouteMux) error { return nil }))
	err := rec.Subscribe(testRegistrySpec("snap.test"), noopHandler, "cg-snap")
	require.NoError(t, err)
	rec.Health("snap-probe", func(context.Context) error { return nil })

	snap := rec.Snapshot()

	// Mutate snap fields.
	snap.RouteGroups = append(snap.RouteGroups, SingleGroup(PrimaryListener, "/extra", func(RouteMux) error { return nil }))
	snap.Subscriptions = append(snap.Subscriptions, SubscriptionRequest{ConsumerGroup: "injected"})
	snap.HealthCheckers["injected-probe"] = func(context.Context) error { return nil }

	// The recorder's internal counters must remain at the original sizes.
	assert.Len(t, rec.routeGroups, 1, "snap mutation must not affect recorder.routeGroups")
	assert.Len(t, rec.subscriptions, 1, "snap mutation must not affect recorder.subscriptions")
	assert.Len(t, rec.healthCheckers, 1, "snap mutation must not affect recorder.healthCheckers")
}

// Compile-time: RouteGroup.Register accepts RouteMux.
var _ = RouteGroup{Register: func(m RouteMux) error {
	m.Handle("GET /", http.NotFoundHandler())
	return nil
}}
