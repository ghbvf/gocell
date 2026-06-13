package devicecommand

// cross_cell_dedup_e2e_test.go — cross-cell HTTP idempotency consumer end-to-end
// (#1610).
//
// Proves the consumer half of #1610: the SAME logical command, submitted over
// HTTP with the same Idempotency-Key for the same device but routed to TWO
// different pods/cells (an LB retry, or two cells that both accept the command),
// is dispatched EXACTLY ONCE.
//
// Each pod is a distinct devicecmd.Service + this slice's HTTP mux wrapped with
// the real HTTP idempotency middleware, writing into ONE shared command outbox
// store (= shared Redis). Each pod has its OWN HTTP idempotency store, so the
// path-scoped HTTP layer does NOT dedup across pods (both handlers run, both
// emit) — any dedup MUST therefore come from the COMMAND layer: both emits carry
// command_id == the client Idempotency-Key, so DeriveCommandKey(tenant, deviceID,
// key) collides and the relay's Claimer wrap runs the enqueue handler once.

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/devicecmd"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/dto"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/mem"
	enqueue "github.com/ghbvf/gocell/generated/contracts/command/devicecommand/enqueue/v1"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/command/commandtest"
	"github.com/ghbvf/gocell/kernel/idempotency"
	kout "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
	runtimecommand "github.com/ghbvf/gocell/runtime/command"
	idemhttp "github.com/ghbvf/gocell/runtime/http/idempotency"
	"github.com/ghbvf/gocell/runtime/outbox"
	"github.com/ghbvf/gocell/runtime/outbox/outboxtest"
)

// countingEnqueueHandler counts enqueue-command dispatches so the test can assert
// relay-level cross-cell deduplication (handler must fire exactly once).
type countingEnqueueHandler struct{ calls int }

func (h *countingEnqueueHandler) HandleEnqueue(_ context.Context, _ *enqueue.Request) (*enqueue.Response, error) {
	h.calls++
	return &enqueue.Response{Data: &enqueue.ResponseData{ID: "cmd-1", Status: "Pending"}}, nil
}

// newAsyncPod builds one "pod/cell": a devicecmd.Service whose EnqueueAsync emits
// into the shared command store, fronted by this slice's HTTP mux wrapped in the
// real HTTP idempotency middleware. Each pod gets its OWN HTTP idempotency store
// so cross-pod dedup can only come from the command layer.
func newAsyncPod(t *testing.T, store *outboxtest.FakeStore) http.Handler {
	t.Helper()
	we, err := kout.NewWriterEmitter(store)
	require.NoError(t, err)
	codec, err := query.NewCursorCodec(bytes.Repeat([]byte("k"), 32))
	require.NoError(t, err)
	svc, err := devicecmd.NewService(
		clock.Real(), commandtest.NewInMemQueue(), mem.NewDeviceRepository(),
		codec, slog.Default(), query.RunModeProd,
		devicecmd.WithSliceName("devicecommand"),
		devicecmd.WithCommandEmitter(kout.WrapEmitterForCell(we)),
	)
	require.NoError(t, err)
	mux := celltest.NewTestMux()
	mux.Route("/api/v1/devices", func(sub cell.RouteMux) {
		require.NoError(t, NewHandler(svc).RegisterRoutes(sub))
	})
	return idemhttp.Middleware(clock.Real(), idemhttp.NewMemStore(clock.Real()))(mux)
}

// postAsync sends an async-enqueue request to a pod with the given Idempotency-Key
// and asserts 202 Accepted.
func postAsync(t *testing.T, pod http.Handler, deviceID, idemKey string) {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices/"+deviceID+"/async-commands",
		strings.NewReader(`{"commandType":"reboot","payload":"now"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", idemKey)
	req = req.WithContext(auth.TestContext("admin-user", []string{dto.RoleAdmin}))
	pod.ServeHTTP(w, req)
	require.Equal(t, http.StatusAccepted, w.Code, "async enqueue should be accepted (202)")
}

// newAsyncRelay builds a relay over store wired with the Claimer-backed command
// dispatch for the generated enqueue command (production composition-root shape).
func newAsyncRelay(t *testing.T, store *outboxtest.FakeStore, reg *runtimecommand.Registry) *outbox.Relay {
	t.Helper()
	relay := outbox.NewRelay(clock.Real(), store, &kout.DiscardPublisher{},
		outbox.RelayConfig{
			PollInterval:   5 * time.Millisecond,
			BaseRetryDelay: 5 * time.Millisecond,
		}.WithDefaults())
	relay.WithCommandDispatch(reg, map[runtimecommand.CommandID]runtimecommand.AsyncDispatchFunc{
		enqueue.DispatchID: enqueue.DispatchAsync,
	}, idempotency.NewInMemClaimer(clock.Real()))
	return relay
}

// TestCrossCell_HTTPAsyncEnqueue_SameSlotDedup is the #1610 consumer e2e: the same
// Idempotency-Key + device, submitted to two different pods, dispatches once.
func TestCrossCell_HTTPAsyncEnqueue_SameSlotDedup(t *testing.T) {
	t.Parallel()

	store := outboxtest.NewFakeStore() // shared command outbox = shared Redis

	reg := runtimecommand.NewRegistry()
	h := &countingEnqueueHandler{}
	require.NoError(t, enqueue.Register(reg, h))

	podA := newAsyncPod(t, store)
	podB := newAsyncPod(t, store)

	const idemKey = "client-req-42"
	// Same logical request, routed to two different pods/cells (e.g. an LB retry).
	postAsync(t, podA, "d1", idemKey)
	postAsync(t, podB, "d1", idemKey)

	// Both pods emitted (HTTP layer did NOT dedup across pods), but the two command
	// entries derive the SAME command-layer dedup slot.
	rows := store.Snapshot()
	require.Len(t, rows, 2, "each pod writes one command entry")
	cmd0, ok0 := runtimecommand.ClaimKeyFromEntry(rows[0].Entry)
	cmd1, ok1 := runtimecommand.ClaimKeyFromEntry(rows[1].Entry)
	require.True(t, ok0 && ok1)
	assert.Equal(t, cmd0, cmd1,
		"same Idempotency-Key + device across pods must derive the SAME dedup slot")
	assert.NotEqual(t, rows[0].Entry.ID(), rows[1].Entry.ID(), "store ids must differ (two writes)")

	relay := newAsyncRelay(t, store, reg)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	go func() { _ = relay.Start(ctx) }()

	require.NoError(t, store.WaitFor(ctx, func(rows []outboxtest.FakeRow) bool {
		return len(rows) == 2 &&
			rows[0].Status == kout.StatePublished &&
			rows[1].Status == kout.StatePublished
	}), "both command entries must settle to published (one dispatched, one deduped)")

	assert.Equal(t, 1, h.calls,
		"cross-cell: the enqueue handler must run exactly once for the same logical command")
}
