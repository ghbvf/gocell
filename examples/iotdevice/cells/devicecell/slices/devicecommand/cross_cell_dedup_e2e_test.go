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
// the real HTTP idempotency middleware (built via newSeededAsyncHandler), writing
// into ONE shared command outbox store (= shared Redis). Each pod has its OWN HTTP
// idempotency store, so the path-scoped HTTP layer does NOT dedup across pods (both
// handlers run, both emit) — any dedup MUST therefore come from the COMMAND layer:
// both emits carry command_id == the client Idempotency-Key, so DeriveCommandKey(
// tenant, deviceID, key) collides and the relay's Claimer wrap runs the enqueue
// handler once.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/dto"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/idempotency"
	kout "github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testtime"
	runtimecommand "github.com/ghbvf/gocell/framework/runtime/command"
	idemhttp "github.com/ghbvf/gocell/framework/runtime/http/idempotency"
	"github.com/ghbvf/gocell/framework/runtime/outbox"
	"github.com/ghbvf/gocell/framework/runtime/outbox/outboxtest"
	cmdremote "github.com/ghbvf/gocell/generated/contracts/command/remotecommand/v1"
)

// countingRemoteCommandHandler counts enqueue-command dispatches so the test can assert
// relay-level cross-cell deduplication (handler must fire exactly once). calls is
// atomic because the relay may dispatch entries from a batch concurrently.
type countingRemoteCommandHandler struct{ calls atomic.Int64 }

func (h *countingRemoteCommandHandler) HandleRemotecommand(_ context.Context, _ *cmdremote.Request) (*cmdremote.Response, error) {
	h.calls.Add(1)
	return &cmdremote.Response{Data: &cmdremote.ResponseData{ID: "cmd-1", Status: "Pending"}}, nil
}

// newAsyncPod builds one "pod/cell": this slice's async-enqueue mux (device "d1"
// seeded) whose EnqueueAsync emits into the shared command store, wrapped in the
// real HTTP idempotency middleware with its OWN idempotency store so cross-pod
// dedup can only come from the command layer.
func newAsyncPod(t *testing.T, store *outboxtest.FakeStore) http.Handler {
	t.Helper()
	we, err := kout.NewWriterEmitter(store)
	require.NoError(t, err)
	mux := newSeededAsyncHandler(t, kout.WrapEmitterForCell(we), "d1")
	return idemhttp.Middleware(clock.Real(), idemhttp.NewMemStore(clock.Real()))(mux)
}

// postAsync sends an async-enqueue request to a pod with the given Idempotency-Key
// (fixed body) and asserts 202 Accepted.
func postAsync(t *testing.T, pod http.Handler, deviceID, idemKey string) {
	t.Helper()
	postAsyncBody(t, pod, deviceID, idemKey, `{"commandType":"reboot","payload":"now"}`)
}

// postAsyncBody is postAsync with an explicit request body (to vary the payload
// fingerprint across requests).
func postAsyncBody(t *testing.T, pod http.Handler, deviceID, idemKey, body string) {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/devices/"+deviceID+"/async-commands",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", idemKey)
	req = req.WithContext(withTestAuth("admin-user", []string{dto.RoleAdmin}))
	pod.ServeHTTP(w, req)
	require.Equal(t, http.StatusAccepted, w.Code, "async enqueue should be accepted (202)")
}

// newAsyncRelay builds a relay over store wired with the Claimer-backed command
// dispatch for the generated enqueue command (production composition-root shape).
func newAsyncRelay(t *testing.T, store *outboxtest.FakeStore, reg *runtimecommand.Registry) *outbox.Relay {
	t.Helper()
	relay := outbox.NewRelay(clock.Real(), store, &kout.DiscardPublisher{},
		outbox.RelayConfig{
			PollInterval:   testtime.FastPoll,
			BaseRetryDelay: testtime.FastPoll,
		}.WithDefaults())
	relay.WithCommandDispatch(reg, map[runtimecommand.CommandID]runtimecommand.AsyncDispatchFunc{
		cmdremote.DispatchID: cmdremote.DispatchAsync,
	}, idempotency.NewInMemClaimer(clock.Real()))
	return relay
}

// TestCrossCell_HTTPAsyncEnqueue_SameSlotDedup is the #1610 consumer e2e: the same
// Idempotency-Key + device, submitted to two different pods, dispatches once.
func TestCrossCell_HTTPAsyncEnqueue_SameSlotDedup(t *testing.T) {
	t.Parallel()

	store := outboxtest.NewFakeStore() // shared command outbox = shared Redis

	reg := runtimecommand.NewRegistry()
	h := &countingRemoteCommandHandler{}
	require.NoError(t, cmdremote.Register(reg, h))

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
	ctx, cancel := context.WithTimeout(context.Background(), testtime.EventuallyDefault)
	defer cancel()
	go func() { _ = relay.Start(ctx) }()

	require.NoError(t, store.WaitFor(ctx, func(rows []outboxtest.FakeRow) bool {
		return len(rows) == 2 &&
			rows[0].Status == kout.StatePublished &&
			rows[1].Status == kout.StatePublished
	}), "both command entries must settle to published (one dispatched, one deduped)")

	assert.Equal(t, int64(1), h.calls.Load(),
		"cross-cell: the enqueue handler must run exactly once for the same logical command")
}

// TestCrossCell_HTTPAsyncEnqueue_DifferentPayloadNotFolded is the #1610 F1 proof:
// the SAME Idempotency-Key with DIFFERENT payloads, submitted to two pods (so the
// per-pod HTTP idempotency layer never sees both and cannot 422 the fingerprint
// mismatch), must NOT be folded by the command layer — the composite dedup token
// includes the payload fingerprint, so each dispatches.
func TestCrossCell_HTTPAsyncEnqueue_DifferentPayloadNotFolded(t *testing.T) {
	t.Parallel()

	store := outboxtest.NewFakeStore()
	reg := runtimecommand.NewRegistry()
	h := &countingRemoteCommandHandler{}
	require.NoError(t, cmdremote.Register(reg, h))

	podA := newAsyncPod(t, store)
	podB := newAsyncPod(t, store)

	const idemKey = "same-key-diff-payload"
	postAsyncBody(t, podA, "d1", idemKey, `{"commandType":"reboot","payload":"AAA"}`)
	postAsyncBody(t, podB, "d1", idemKey, `{"commandType":"reboot","payload":"BBB"}`)

	rows := store.Snapshot()
	require.Len(t, rows, 2)
	cmd0, ok0 := runtimecommand.ClaimKeyFromEntry(rows[0].Entry)
	cmd1, ok1 := runtimecommand.ClaimKeyFromEntry(rows[1].Entry)
	require.True(t, ok0 && ok1)
	assert.NotEqual(t, cmd0, cmd1,
		"#1610 F1: same key + DIFFERENT payload must NOT fold to the same dedup slot")

	relay := newAsyncRelay(t, store, reg)
	ctx, cancel := context.WithTimeout(context.Background(), testtime.EventuallyDefault)
	defer cancel()
	go func() { _ = relay.Start(ctx) }()

	require.NoError(t, store.WaitFor(ctx, func(rows []outboxtest.FakeRow) bool {
		return len(rows) == 2 &&
			rows[0].Status == kout.StatePublished &&
			rows[1].Status == kout.StatePublished
	}), "both distinct commands must settle to published")

	assert.Equal(t, int64(2), h.calls.Load(),
		"#1610 F1: different payloads (same key) must each dispatch — not deduped")
}
