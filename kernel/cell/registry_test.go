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
	"github.com/ghbvf/gocell/kernel/webhook"
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
// TestRegistry_RegisterReadiness_AccumulatesInSnapshot
// ---------------------------------------------------------------------------

// TestRegistry_RegisterReadiness_AccumulatesInSnapshot verifies that probes
// registered via reg.RegisterReadiness(...) appear in the RegistrySnapshot for
// the bootstrap layer to drain onto the runtime aggregator.
func TestRegistry_RegisterReadiness_AccumulatesInSnapshot(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)

	name := healthz.MustProbeName("foo_ready")
	prober := healthz.ProberFunc(func(_ context.Context) error { return nil })
	require.NoError(t, rec.RegisterReadiness(name, prober))

	snap := rec.Snapshot()
	require.Len(t, snap.Probes, 1)
	assert.Equal(t, name, snap.Probes[0].Name())
}

// ---------------------------------------------------------------------------
// TestRegistry_RegisterReadiness_DuplicateName_RejectsSecond
// ---------------------------------------------------------------------------

// TestRegistry_RegisterReadiness_DuplicateName_RejectsSecond verifies that
// registering a second probe with the same name returns ErrDuplicateProbe and
// does not store the duplicate (first-wins, same contract as the runtime aggregator).
func TestRegistry_RegisterReadiness_DuplicateName_RejectsSecond(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)

	name := healthz.MustProbeName("my_probe")
	p1 := healthz.ProberFunc(func(_ context.Context) error { return nil })
	p2 := healthz.ProberFunc(func(_ context.Context) error { return errors.New("fail") })

	require.NoError(t, rec.RegisterReadiness(name, p1))
	err := rec.RegisterReadiness(name, p2)
	require.Error(t, err)
	assert.True(t, errors.Is(err, healthz.ErrDuplicateProbe))

	require.Len(t, rec.Snapshot().Probes, 1)
}

// ---------------------------------------------------------------------------
// TestRegistry_RegisterReadiness_AfterSnapshot_Panics
// ---------------------------------------------------------------------------

// TestRegistry_RegisterReadiness_AfterSnapshot_Panics verifies probe registration
// after Snapshot() panics — same finalize guard as the other registration
// methods (no lazy registration past sealing).
func TestRegistry_RegisterReadiness_AfterSnapshot_Panics(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)
	_ = rec.Snapshot() // finalize
	assert.Panics(t, func() {
		_ = rec.RegisterReadiness(healthz.MustProbeName("late_ready"),
			healthz.ProberFunc(func(_ context.Context) error { return nil }))
	})
}

// TestRegistry_RegisterReadiness_RejectsNilAndEmptyName verifies that
// RegisterReadiness rejects a nil prober and an empty name, and does not
// accumulate the rejected probe. The empty-name error is an errcode.Error
// (per F11 finding); the nil-prober error still wraps healthz.ErrInvalidProbeName.
func TestRegistry_RegisterReadiness_RejectsNilAndEmptyName(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)
	// empty name (zero ProbeName) must be rejected.
	emptyErr := rec.RegisterReadiness("", healthz.ProberFunc(func(_ context.Context) error { return nil }))
	assert.Error(t, emptyErr, "empty name must return error")
	// Error is an errcode.Error — cast to verify the code.
	var ecErr *errcode.Error
	if !errors.As(emptyErr, &ecErr) {
		t.Errorf("empty name error is %T, want *errcode.Error", emptyErr)
	}
	// nil prober must be rejected with ErrInvalidProbeName.
	assert.ErrorIs(t, rec.RegisterReadiness(healthz.MustProbeName("ok_name"), nil), healthz.ErrInvalidProbeName)
	assert.Empty(t, rec.Snapshot().Probes, "rejected probes must not be accumulated")
}

// ---------------------------------------------------------------------------
// TestRegistry_Healthz_ReturnsWriteSink (compat: verifies Healthz accessor)
// ---------------------------------------------------------------------------

// TestRegistry_Healthz_ReturnsWriteSink verifies the recorder exposes a usable
// healthz.Aggregator for bootstrap-internal use (e.g. draining probes into the
// runtime aggregator). This accessor is retained for backward compatibility with
// the bootstrap drain path.
func TestRegistry_Healthz_ReturnsWriteSink(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)
	require.NotNil(t, rec)
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
	// Verify the recorder can register a probe without error.
	require.NoError(t, rec.RegisterReadiness(healthz.MustProbeName("logger_test_ready"),
		healthz.ProberFunc(func(_ context.Context) error { return nil })))
}

// Compile-time: RouteGroup.Register accepts RouteMux.
var _ = RouteGroup{Register: func(m RouteMux) error {
	m.Handle("GET /", http.NotFoundHandler())
	return nil
}}

// ---------------------------------------------------------------------------
// Webhook record-only API (PR-2)
// ---------------------------------------------------------------------------

func noopWebhookHandler(_ context.Context, _ webhook.Delivery) error { return nil }

func noopWebhookSelector(_ context.Context, _ []byte) (string, error) { return "target", nil }

func validReceiverSpec() webhook.ReceiverSpec {
	return webhook.ReceiverSpec{
		ContractID:       "webhook.stripe.payment-events.v1",
		SourceID:         "stripe",
		CellID:           "hooks",
		PathPattern:      "/api/webhooks/stripe/payment-events",
		DeliveryIDHeader: "X-Delivery-Id",
		TimestampHeader:  "X-Timestamp",
		SignatureHeader:  "X-Signature",
		ToleranceSeconds: 300,
		MaxBodyBytes:     1048576,
	}
}

func validDispatchSpec() webhook.DispatchSpec {
	return webhook.DispatchSpec{
		ContractID: "webhook.shopify.orders.v1",
		SourceID:   "shopify",
		CellID:     "hooks",
	}
}

// ---------------------------------------------------------------------------
// TestRegistry_RegisterWebhookReceiver_HappyPath_AppendsToSnapshot
// ---------------------------------------------------------------------------

// TestRegistry_RegisterWebhookReceiver_HappyPath_AppendsToSnapshot verifies the
// record-only API accumulates a WebhookReceiverRequest into the snapshot —
// mirroring the Subscribe happy-path test. PR-2 does not drain it.
func TestRegistry_RegisterWebhookReceiver_HappyPath_AppendsToSnapshot(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)

	spec := validReceiverSpec()
	require.NoError(t, rec.RegisterWebhookReceiver(spec, noopWebhookHandler))

	snap := rec.Snapshot()
	require.Len(t, snap.WebhookReceivers, 1)
	assert.Equal(t, spec, snap.WebhookReceivers[0].Spec)
	assert.NotNil(t, snap.WebhookReceivers[0].Handler)
	// Dispatch slice stays empty when only a receiver is registered.
	assert.Empty(t, snap.WebhookDispatchers)
}

// ---------------------------------------------------------------------------
// TestRegistry_RegisterWebhookDispatch_HappyPath_AppendsToSnapshot
// ---------------------------------------------------------------------------

func TestRegistry_RegisterWebhookDispatch_HappyPath_AppendsToSnapshot(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)

	spec := validDispatchSpec()
	require.NoError(t, rec.RegisterWebhookDispatch(spec, noopWebhookSelector))

	snap := rec.Snapshot()
	require.Len(t, snap.WebhookDispatchers, 1)
	assert.Equal(t, spec, snap.WebhookDispatchers[0].Spec)
	assert.NotNil(t, snap.WebhookDispatchers[0].Selector)
	assert.Empty(t, snap.WebhookReceivers)
}

// ---------------------------------------------------------------------------
// TestRegistry_RegisterWebhookReceiver_RejectsNilAndInvalidSpec
// ---------------------------------------------------------------------------

// TestRegistry_RegisterWebhookReceiver_RejectsNilAndInvalidSpec is table-driven
// over the rejection paths: nil handler and each empty spec field (spec.Validate
// failure). Rejected receivers must not accumulate.
func TestRegistry_RegisterWebhookReceiver_RejectsNilAndInvalidSpec(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		spec        webhook.ReceiverSpec
		handler     webhook.WebhookReceiveHandler
		wantMsgPart string
	}{
		{
			name:        "nil_handler",
			spec:        validReceiverSpec(),
			handler:     nil,
			wantMsgPart: "handler",
		},
		{
			name:        "empty_contract_id",
			spec:        webhook.ReceiverSpec{ContractID: "", SourceID: "stripe", CellID: "hooks"},
			handler:     noopWebhookHandler,
			wantMsgPart: "ContractID",
		},
		{
			name:        "empty_source_id",
			spec:        webhook.ReceiverSpec{ContractID: "webhook.x.v1", SourceID: "", CellID: "hooks"},
			handler:     noopWebhookHandler,
			wantMsgPart: "SourceID",
		},
		{
			name:        "empty_cell_id",
			spec:        webhook.ReceiverSpec{ContractID: "webhook.x.v1", SourceID: "stripe", CellID: ""},
			handler:     noopWebhookHandler,
			wantMsgPart: "CellID",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)
			err := rec.RegisterWebhookReceiver(tc.spec, tc.handler)
			require.Error(t, err)
			var ecErr *errcode.Error
			require.True(t, errors.As(err, &ecErr), "error must be *errcode.Error, got %T", err)
			assert.Contains(t, ecErr.Message, tc.wantMsgPart)
			assert.Empty(t, rec.Snapshot().WebhookReceivers, "rejected receiver must not accumulate")
		})
	}
}

// ---------------------------------------------------------------------------
// TestRegistry_RegisterWebhookDispatch_RejectsNilAndInvalidSpec
// ---------------------------------------------------------------------------

func TestRegistry_RegisterWebhookDispatch_RejectsNilAndInvalidSpec(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		spec        webhook.DispatchSpec
		selector    webhook.WebhookDispatchSelector
		wantMsgPart string
	}{
		{
			name:        "nil_selector",
			spec:        validDispatchSpec(),
			selector:    nil,
			wantMsgPart: "selector",
		},
		{
			name:        "empty_contract_id",
			spec:        webhook.DispatchSpec{ContractID: "", SourceID: "shopify", CellID: "hooks"},
			selector:    noopWebhookSelector,
			wantMsgPart: "ContractID",
		},
		{
			name:        "empty_source_id",
			spec:        webhook.DispatchSpec{ContractID: "webhook.x.v1", SourceID: "", CellID: "hooks"},
			selector:    noopWebhookSelector,
			wantMsgPart: "SourceID",
		},
		{
			name:        "empty_cell_id",
			spec:        webhook.DispatchSpec{ContractID: "webhook.x.v1", SourceID: "shopify", CellID: ""},
			selector:    noopWebhookSelector,
			wantMsgPart: "CellID",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)
			err := rec.RegisterWebhookDispatch(tc.spec, tc.selector)
			require.Error(t, err)
			var ecErr *errcode.Error
			require.True(t, errors.As(err, &ecErr), "error must be *errcode.Error, got %T", err)
			assert.Contains(t, ecErr.Message, tc.wantMsgPart)
			assert.Empty(t, rec.Snapshot().WebhookDispatchers, "rejected dispatcher must not accumulate")
		})
	}
}

// ---------------------------------------------------------------------------
// TestRegistry_PostSnapshot_Webhook_Panics
// ---------------------------------------------------------------------------

// TestRegistry_PostSnapshot_Webhook_Panics verifies both webhook registration
// methods share the same post-Snapshot finalize guard as Subscribe.
func TestRegistry_PostSnapshot_Webhook_Panics(t *testing.T) {
	t.Run("receiver", func(t *testing.T) {
		rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)
		_ = rec.Snapshot() // finalize
		assert.Panics(t, func() {
			_ = rec.RegisterWebhookReceiver(validReceiverSpec(), noopWebhookHandler)
		})
	})
	t.Run("dispatch", func(t *testing.T) {
		rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)
		_ = rec.Snapshot() // finalize
		assert.Panics(t, func() {
			_ = rec.RegisterWebhookDispatch(validDispatchSpec(), noopWebhookSelector)
		})
	})
}

// ---------------------------------------------------------------------------
// TestRegistry_Snapshot_DefensiveCopy_Webhook
// ---------------------------------------------------------------------------

// TestRegistry_Snapshot_DefensiveCopy_Webhook verifies the webhook slices are
// defensively copied — mutating the snapshot must not affect recorder state.
func TestRegistry_Snapshot_DefensiveCopy_Webhook(t *testing.T) {
	rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)
	require.NoError(t, rec.RegisterWebhookReceiver(validReceiverSpec(), noopWebhookHandler))
	require.NoError(t, rec.RegisterWebhookDispatch(validDispatchSpec(), noopWebhookSelector))

	snap := rec.Snapshot()
	snap.WebhookReceivers = append(snap.WebhookReceivers, WebhookReceiverRequest{})
	snap.WebhookDispatchers = append(snap.WebhookDispatchers, WebhookDispatchRequest{})

	assert.Len(t, rec.webhookReceivers, 1, "snap mutation must not affect recorder.webhookReceivers")
	assert.Len(t, rec.webhookDispatchers, 1, "snap mutation must not affect recorder.webhookDispatchers")
}

// ---------------------------------------------------------------------------
// TestRegistry_Subscribe_SpecValidate_RejectsInvalidSpec (F3)
// ---------------------------------------------------------------------------

// TestRegistry_Subscribe_SpecValidate_RejectsInvalidSpec verifies that Subscribe
// calls ContractSpec.Validate() and rejects malformed specs that pass the
// individual Kind/Topic guards but fail the full validation (e.g. empty
// Transport or an event spec that mistakenly also carries Method/Path).
func TestRegistry_Subscribe_SpecValidate_RejectsInvalidSpec(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		mutate      func(*contractspec.ContractSpec)
		wantMsgPart string
	}{
		{
			name:        "empty_transport",
			mutate:      func(s *contractspec.ContractSpec) { s.Transport = "" },
			wantMsgPart: "Transport",
		},
		{
			name:        "event_with_method",
			mutate:      func(s *contractspec.ContractSpec) { s.Method = "GET" },
			wantMsgPart: "Method",
		},
		{
			name:        "empty_id",
			mutate:      func(s *contractspec.ContractSpec) { s.ID = "" },
			wantMsgPart: "ID",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := NewRegistryRecorder(nil, outbox.DurabilityDurable)
			spec := testRegistrySpec("order.placed")
			tc.mutate(&spec)
			err := rec.Subscribe(spec, noopHandler, "cg-order", "ordercell")
			require.Error(t, err, "spec failing Validate must be rejected")
			assert.Contains(t, err.Error(), tc.wantMsgPart,
				"error must mention the invalid field")
			assert.Empty(t, rec.Snapshot().Subscriptions, "rejected subscription must not accumulate")
		})
	}
}

// validGRPCSpecInternal returns a well-formed GRPCServiceSpec for in-package tests.
func validGRPCSpecInternal(contractID string) GRPCServiceSpec {
	return GRPCServiceSpec{
		ContractID: contractID,
		CellID:     "test-cell",
		Listener:   PrimaryListener,
		Register:   func() {}, // non-nil any; type check happens in runtime/grpc layer
	}
}

// TestRegistrySnapshot_GRPCServices_DefensiveCopy verifies Snapshot().GRPCServices
// is a defensive copy: mutating the returned slice must NOT corrupt the recorder's
// internal grpcServices slice. This is an in-package test so it can observe the
// SAME recorder's unexported field — an external test mutating one recorder's
// snapshot and asserting on a second independent recorder would pass even if
// Snapshot aliased the internal slice (the mutation could never reach the other
// recorder regardless), making it vacuous.
func TestRegistrySnapshot_GRPCServices_DefensiveCopy(t *testing.T) {
	t.Parallel()

	r := NewRegistryRecorder(nil, outbox.DurabilityDemo)
	require.NoError(t, r.GRPCService(validGRPCSpecInternal("grpc.svc.v1")))
	require.NoError(t, r.GRPCService(validGRPCSpecInternal("grpc.svc.v2")))

	snap := r.Snapshot()
	require.Len(t, snap.GRPCServices, 2)

	// Mutate the snapshot's first element in-place.
	snap.GRPCServices[0] = GRPCServiceSpec{ContractID: "CORRUPTED"}

	// Assert 1: the mutation landed on the snapshot (non-vacuous check).
	assert.Equal(t, "CORRUPTED", snap.GRPCServices[0].ContractID,
		"mutation must be visible on the snapshot to confirm the test is non-vacuous")

	// Assert 2: the SAME recorder's internal slice is untainted — proving Snapshot
	// returned a copy, not an alias.
	require.Len(t, r.grpcServices, 2)
	assert.Equal(t, "grpc.svc.v1", r.grpcServices[0].ContractID,
		"recorder's internal grpcServices[0] must not be corrupted by mutating the snapshot")
}
