package bootstrap

// phases_webhook_test.go — unit + end-to-end tests for phase5DrainWebhookReceivers.
//
// Coverage:
//   - empty receiver set → no-op, no errors
//   - non-empty without WithWebhookSourceStore → fail-fast errcode
//   - non-empty without WithWebhookClaimer → fail-fast errcode
//   - CellID drift → fail-fast error
//   - end-to-end: snapshot with 1 receiver + WithWebhookSourceStore/Claimer →
//     router mounted at spec.PathPattern; valid HMAC → 200; bad HMAC → 401

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/kernel/outbox"
	kwh "github.com/ghbvf/gocell/kernel/webhook"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/http/router"
)

// ---------------------------------------------------------------------------
// Test fixtures
// ---------------------------------------------------------------------------

const (
	whTestCellID       = "webhookcell"
	whTestContractID   = "webhook.bootstrap-phase5.v1"
	whTestSourceID     = "test-source"
	whTestPathPattern  = "/api/webhooks/bootstrap-test"
	whTestSecret       = "12345678901234567890abcd" // 24 bytes
	whTestTolerance    = 300                        // seconds
	whTestMaxBodyBytes = 1 << 20                    // 1 MiB
)

var whFixedNow = time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)

func whTestSpec() kwh.ReceiverSpec {
	return kwh.ReceiverSpec{
		ContractID:       whTestContractID,
		SourceID:         whTestSourceID,
		CellID:           whTestCellID,
		PathPattern:      whTestPathPattern,
		DeliveryIDHeader: "X-Delivery-Id",
		TimestampHeader:  "X-Timestamp",
		SignatureHeader:  "X-Signature",
		ToleranceSeconds: whTestTolerance,
		MaxBodyBytes:     whTestMaxBodyBytes,
	}
}

func whTestSource(t *testing.T) kwh.Source {
	t.Helper()
	id, err := kwh.NewSourceID(whTestSourceID)
	require.NoError(t, err, "NewSourceID")
	src, err := kwh.NewSource(id, []byte(whTestSecret))
	require.NoError(t, err, "NewSource")
	return src
}

func whTestStore(t *testing.T) *kwh.SourceRegistry {
	t.Helper()
	reg := kwh.NewSourceRegistry()
	require.NoError(t, reg.Register(whTestSource(t)), "Register source")
	return reg
}

// webhookTestCell calls reg.RegisterWebhookReceiver in its Init so bootstrap
// captures a WebhookReceiverRequest in the RegistrySnapshot.
type webhookTestCell struct {
	*cell.BaseCell
	spec    kwh.ReceiverSpec
	handler kwh.WebhookReceiveHandler
	// If non-zero, override the CellID in the spec (to simulate drift).
	overrideCellID string
}

func (c *webhookTestCell) Init(ctx context.Context, reg cell.Registrar) error {
	if err := c.BaseCell.Init(ctx, reg); err != nil {
		return err
	}
	spec := c.spec
	if c.overrideCellID != "" {
		spec.CellID = c.overrideCellID
	}
	return reg.RegisterWebhookReceiver(spec, c.handler)
}

// newWebhookCell creates a webhookTestCell with the given spec.
// The cell ID is always taken from spec.CellID so that the snapshot key
// matches what the receiver declares.
func newWebhookCell(spec kwh.ReceiverSpec, handler kwh.WebhookReceiveHandler) *webhookTestCell {
	return &webhookTestCell{
		BaseCell: cell.MustNewBaseCell(&metadata.CellMeta{
			ID:   spec.CellID,
			Type: "core",
		}),
		spec:    spec,
		handler: handler,
	}
}

// buildPhaseStateWithWebhookCell starts an assembly containing cells, returns a
// phaseState with asm and cellSnapshots populated (mirrors phase3InitAssembly).
func buildPhaseStateWithWebhookCells(t *testing.T, cells ...cell.Cell) *phaseState {
	t.Helper()
	asm := assembly.New(clockmock.New(whFixedNow), assembly.Config{
		ID:             "webhook-test-asm",
		DurabilityMode: outbox.DurabilityDemo,
	})
	for _, c := range cells {
		require.NoErrorf(t, asm.Register(c), "Register cell %q", c.ID())
	}
	require.NoError(t, asm.Start(context.Background()), "asm.Start")
	t.Cleanup(func() { _ = asm.Stop(context.Background()) })

	_, s := newPhaseState()
	s.asm = asm
	s.cellSnapshots = asm.Snapshots()
	return s
}

// noopWebhookHandler is a no-op receiver used for tests that don't exercise handler logic.
func noopWebhookHandler(_ context.Context, _ kwh.Delivery) error { return nil }

// ---------------------------------------------------------------------------
// Phase5DrainWebhookReceivers — unit tests
// ---------------------------------------------------------------------------

// TestPhase5DrainWebhookReceivers_EmptyNoOp verifies that an assembly with no
// webhook receivers produces zero groups and no error.
func TestPhase5DrainWebhookReceivers_EmptyNoOp(t *testing.T) {
	t.Parallel()
	// Plain test cell with no webhook receivers.
	tc := &testCell{
		BaseCell: cell.MustNewBaseCell(&metadata.CellMeta{ID: "plain-cell", Type: "core"}),
	}
	s := buildPhaseStateWithWebhookCells(t, tc)

	b := New(clockmock.New(whFixedNow))
	groups, err := b.phase5DrainWebhookReceivers(s)

	require.NoError(t, err, "empty receivers must not error")
	assert.Empty(t, groups, "empty receivers must produce no groups")
}

// TestPhase5DrainWebhookReceivers_FailsWithoutSourceStore verifies that a
// snapshot with one webhook receiver and no WithWebhookSourceStore causes
// phase5DrainWebhookReceivers to return an errcode with ErrCellInvalidConfig.
func TestPhase5DrainWebhookReceivers_FailsWithoutSourceStore(t *testing.T) {
	t.Parallel()
	wc := newWebhookCell(whTestSpec(), noopWebhookHandler)
	s := buildPhaseStateWithWebhookCells(t, wc)

	b := New(clockmock.New(whFixedNow))
	// webhookClaimer set, but webhookSourceStore is nil.
	b.webhookClaimer = idempotency.NewInMemClaimer(clockmock.New(whFixedNow))

	_, err := b.phase5DrainWebhookReceivers(s)

	require.Error(t, err, "missing source store must error")
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr, "error must be *errcode.Error")
	assert.Equal(t, errcode.ErrCellInvalidConfig, ecErr.Code,
		"missing source store must yield ErrCellInvalidConfig")
	assert.Contains(t, ecErr.Message, "WithWebhookSourceStore",
		"error message must name the missing option")
}

// TestPhase5DrainWebhookReceivers_FailsWithoutClaimer verifies that a snapshot
// with one webhook receiver and no WithWebhookClaimer causes phase5DrainWebhookReceivers
// to return an errcode with ErrCellInvalidConfig.
func TestPhase5DrainWebhookReceivers_FailsWithoutClaimer(t *testing.T) {
	t.Parallel()
	wc := newWebhookCell(whTestSpec(), noopWebhookHandler)
	s := buildPhaseStateWithWebhookCells(t, wc)

	b := New(clockmock.New(whFixedNow))
	// webhookSourceStore set, but webhookClaimer is nil.
	b.webhookSourceStore = whTestStore(t)

	_, err := b.phase5DrainWebhookReceivers(s)

	require.Error(t, err, "missing claimer must error")
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr, "error must be *errcode.Error")
	assert.Equal(t, errcode.ErrCellInvalidConfig, ecErr.Code,
		"missing claimer must yield ErrCellInvalidConfig")
	assert.Contains(t, ecErr.Message, "WithWebhookClaimer",
		"error message must name the missing option")
}

// TestPhase5DrainWebhookReceivers_FailsOnCellIDDrift verifies that a snapshot
// whose webhook receiver has a CellID that doesn't match the snapshot key
// causes a fail-fast drift error.
func TestPhase5DrainWebhookReceivers_FailsOnCellIDDrift(t *testing.T) {
	t.Parallel()
	// Cell registers with ID "webhookcell" but spec.CellID is overridden to "wrongcell".
	wc := &webhookTestCell{
		BaseCell:       cell.MustNewBaseCell(&metadata.CellMeta{ID: whTestCellID, Type: "core"}),
		spec:           whTestSpec(),
		handler:        noopWebhookHandler,
		overrideCellID: "wrongcell",
	}
	s := buildPhaseStateWithWebhookCells(t, wc)

	b := New(clockmock.New(whFixedNow))
	b.webhookSourceStore = whTestStore(t)
	b.webhookClaimer = idempotency.NewInMemClaimer(clockmock.New(whFixedNow))

	_, err := b.phase5DrainWebhookReceivers(s)

	require.Error(t, err, "CellID drift must error")
	assert.Contains(t, err.Error(), "drift", "drift error must mention 'drift'")
	assert.Contains(t, err.Error(), "wrongcell", "drift error must mention the mismatched CellID")
}

// TestPhase5DrainWebhookReceivers_SkipsMissingSnapshot verifies the defensive
// branch where a cell ID returned by asm.CellIDs() has no entry in
// cellSnapshots: the drain loop must `continue` (skip it) rather than panic,
// and — with no other receivers — return an empty no-op result.
func TestPhase5DrainWebhookReceivers_SkipsMissingSnapshot(t *testing.T) {
	t.Parallel()
	wc := newWebhookCell(whTestSpec(), noopWebhookHandler)
	s := buildPhaseStateWithWebhookCells(t, wc)
	// Drop the only cell's snapshot so its ID survives in asm.CellIDs() but the
	// snapshot lookup misses — exercising the `if !ok { continue }` branch.
	delete(s.cellSnapshots, whTestCellID)

	b := New(clockmock.New(whFixedNow))
	groups, err := b.phase5DrainWebhookReceivers(s)

	require.NoError(t, err, "missing snapshot must be skipped, not error")
	assert.Empty(t, groups, "no resolvable receivers must produce zero groups")
}

// TestPhase5DrainWebhookReceivers_WrapsBuildError verifies that a receiver
// request whose spec is invalid (injected past the registry's validation guard
// to reach the build step) causes BuildRouteGroups to fail and phase5 to wrap
// the error with the "build webhook route groups" context.
func TestPhase5DrainWebhookReceivers_WrapsBuildError(t *testing.T) {
	t.Parallel()
	wc := newWebhookCell(whTestSpec(), noopWebhookHandler)
	s := buildPhaseStateWithWebhookCells(t, wc)
	// Replace the validated request with one whose spec is invalid (empty
	// PathPattern) but whose CellID still matches the snapshot key — so it
	// passes the drift check and fails inside BuildRouteGroups → NewReceiver.
	badSpec := whTestSpec()
	badSpec.PathPattern = ""
	snap := s.cellSnapshots[whTestCellID]
	snap.WebhookReceivers = []cell.WebhookReceiverRequest{{
		Spec:    badSpec,
		Handler: noopWebhookHandler,
	}}
	s.cellSnapshots[whTestCellID] = snap

	clk := clockmock.New(whFixedNow)
	b := New(clk)
	b.webhookSourceStore = whTestStore(t)
	b.webhookClaimer = idempotency.NewInMemClaimer(clk)

	_, err := b.phase5DrainWebhookReceivers(s)

	require.Error(t, err, "invalid spec must surface as a build error")
	assert.Contains(t, err.Error(), "build webhook route groups",
		"phase5 must wrap the BuildRouteGroups failure with its own context")
}

// TestPhase5DrainWebhookReceivers_HappyPath_ProducesGroups verifies that a
// well-configured snapshot with one receiver produces one RouteGroup with
// the correct Listener, Prefix, and CellID.
func TestPhase5DrainWebhookReceivers_HappyPath_ProducesGroups(t *testing.T) {
	t.Parallel()
	wc := newWebhookCell(whTestSpec(), noopWebhookHandler)
	s := buildPhaseStateWithWebhookCells(t, wc)

	clk := clockmock.New(whFixedNow)
	b := New(clk)
	b.webhookSourceStore = whTestStore(t)
	b.webhookClaimer = idempotency.NewInMemClaimer(clk)

	groups, err := b.phase5DrainWebhookReceivers(s)

	require.NoError(t, err, "happy path must not error")
	require.Len(t, groups, 1, "must produce one group per receiver")

	g := groups[0]
	assert.Equal(t, cell.PrimaryListener, g.Listener, "webhook groups must target PrimaryListener")
	assert.Equal(t, whTestPathPattern, g.Prefix, "group Prefix must equal PathPattern")
	assert.Equal(t, whTestCellID, g.CellID, "group CellID must match spec.CellID")
	require.NotNil(t, g.Register, "Register func must not be nil")
}

// ---------------------------------------------------------------------------
// End-to-end test: phase5DrainWebhookReceivers → router → real HTTP
// ---------------------------------------------------------------------------

// TestPhase5DrainWebhookReceivers_EndToEnd verifies the full mount pipeline:
//
//  1. phase5DrainWebhookReceivers returns a RouteGroup for the registered receiver.
//  2. MountRouteGroup mounts it on a real Router at the correct prefix (no double-prefix).
//  3. A valid HMAC-signed POST to spec.PathPattern returns 200.
//  4. A request with a forged signature returns 401.
//
// This is the primary correctness gate for the double-prefix fix: the request
// must reach the handler at exactly spec.PathPattern, not at a doubled path.
func TestPhase5DrainWebhookReceivers_EndToEnd(t *testing.T) {
	clk := clockmock.New(whFixedNow)
	src := whTestSource(t)
	store := whTestStore(t)
	claimer := idempotency.NewInMemClaimer(clk)

	wc := newWebhookCell(whTestSpec(), noopWebhookHandler)
	s := buildPhaseStateWithWebhookCells(t, wc)

	b := New(clk)
	b.webhookSourceStore = store
	b.webhookClaimer = claimer

	groups, err := b.phase5DrainWebhookReceivers(s)
	require.NoError(t, err, "phase5DrainWebhookReceivers")
	require.Len(t, groups, 1)

	// Build a real Router and mount the group.
	rtr, err := router.NewForListener(clk, cell.PrimaryListener)
	require.NoError(t, err, "NewForListener")
	require.NoError(t, rtr.MountRouteGroup(groups[0]), "MountRouteGroup")

	// Build a signer for the test source so we can produce valid requests.
	signer, err := kwh.NewHMACSigner(src)
	require.NoError(t, err, "NewHMACSigner")

	body := []byte(`{"event":"bootstrap-e2e-test"}`)
	did, err := kwh.NewDeliveryID("bootstrap-e2e-001")
	require.NoError(t, err, "NewDeliveryID")
	headers, err := signer.Sign(body, whFixedNow, did)
	require.NoError(t, err, "Sign")

	buildReq := func(signature string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, whTestPathPattern, bytes.NewReader(body))
		req.Header.Set("X-Delivery-Id", string(headers.DeliveryID))
		req.Header.Set("X-Timestamp", headers.Timestamp)
		req.Header.Set("X-Signature", signature)
		return req
	}

	t.Run("valid_hmac_returns_200", func(t *testing.T) {
		rec := httptest.NewRecorder()
		rtr.Handler().ServeHTTP(rec, buildReq(headers.Signature))
		assert.Equal(t, http.StatusOK, rec.Code,
			"valid HMAC at %s must return 200 (no double-prefix, handler reached)", whTestPathPattern)
	})

	t.Run("forged_signature_returns_401", func(t *testing.T) {
		rec := httptest.NewRecorder()
		rtr.Handler().ServeHTTP(rec, buildReq("v1,AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="))
		assert.Equal(t, http.StatusUnauthorized, rec.Code,
			"forged HMAC at %s must return 401", whTestPathPattern)
	})
}

// ---------------------------------------------------------------------------
// Options tests: WithWebhookSourceStore / WithWebhookClaimer
// ---------------------------------------------------------------------------

// TestWithWebhookSourceStore_StoresValue verifies the option sets the field.
func TestWithWebhookSourceStore_StoresValue(t *testing.T) {
	store := whTestStore(t)
	b := New(clockmock.New(whFixedNow), WithWebhookSourceStore(store))
	assert.Equal(t, store, b.webhookSourceStore, "WithWebhookSourceStore must store the value")
}

// TestWithWebhookSourceStore_NilIgnored verifies that a typed-nil is silently ignored.
func TestWithWebhookSourceStore_NilIgnored(t *testing.T) {
	b := New(clockmock.New(whFixedNow))
	var nilStore kwh.SourceStore
	opt := WithWebhookSourceStore(nilStore)
	opt(b)
	assert.Nil(t, b.webhookSourceStore, "typed-nil source store must be ignored")
}

// TestWithWebhookClaimer_StoresValue verifies the option sets the field.
func TestWithWebhookClaimer_StoresValue(t *testing.T) {
	clk := clockmock.New(whFixedNow)
	claimer := idempotency.NewInMemClaimer(clk)
	b := New(clk, WithWebhookClaimer(claimer))
	assert.Equal(t, claimer, b.webhookClaimer, "WithWebhookClaimer must store the value")
}

// TestWithWebhookClaimer_NilIgnored verifies that a typed-nil is silently ignored.
func TestWithWebhookClaimer_NilIgnored(t *testing.T) {
	clk := clockmock.New(whFixedNow)
	b := New(clk)
	var nilClaimer idempotency.Claimer
	opt := WithWebhookClaimer(nilClaimer)
	opt(b)
	assert.Nil(t, b.webhookClaimer, "typed-nil claimer must be ignored")
}

// TestWithWebhookSourceStore_BareNilIgnored verifies that a bare untyped nil
// passed directly to WithWebhookSourceStore is silently ignored (cumulative
// option semantics — does not clear a previously set store).
func TestWithWebhookSourceStore_BareNilIgnored(t *testing.T) {
	b := New(clockmock.New(whFixedNow))
	// Pass bare nil (not a typed interface variable).
	opt := WithWebhookSourceStore(nil)
	opt(b)
	assert.Nil(t, b.webhookSourceStore, "bare-nil source store must be ignored")
}

// TestWithWebhookClaimer_BareNilIgnored verifies that a bare untyped nil
// passed directly to WithWebhookClaimer is silently ignored.
func TestWithWebhookClaimer_BareNilIgnored(t *testing.T) {
	clk := clockmock.New(whFixedNow)
	b := New(clk)
	// Pass bare nil (not a typed interface variable).
	opt := WithWebhookClaimer(nil)
	opt(b)
	assert.Nil(t, b.webhookClaimer, "bare-nil claimer must be ignored")
}

// Compile-time check: webhookTestCell implements cell.Cell.
var _ cell.Cell = (*webhookTestCell)(nil)
