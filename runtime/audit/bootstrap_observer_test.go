package audit_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/audit"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
)

// failingStore is a ledger.Store that returns an injected Append error so the
// observer's double-write fallback path can be asserted in isolation.
type failingStore struct {
	ledger.Store
	appendErr error
}

func (f failingStore) Append(_ context.Context, _ *ledger.Entry) error {
	return f.appendErr
}

// TestNewBootstrapAuthFailObserver_DoubleWriteSlogAndAudit covers T4.
// Observer returned by NewBootstrapAuthFailObserver writes BOTH the slog
// "bootstrap_auth_failed" line (with client_ip from ctx) AND a hash-chain
// entry to the injected ledger.Store.
func TestNewBootstrapAuthFailObserver_DoubleWriteSlogAndAudit(t *testing.T) {
	t.Parallel()
	store, clk := buildTestLedgerStore(t)

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	obs, err := audit.NewBootstrapAuthFailObserver(logger, store, clk)
	require.NoError(t, err)
	require.NotNil(t, obs)

	ctx := ctxkeys.WithRealIP(context.Background(), "192.0.2.99")
	obs(ctx, "wrong_credentials")

	// slog channel: must include the standard label, the reason, and the
	// client_ip captured from context — the wire shape access_module's old
	// test asserted on, now owned by the centralized funnel.
	logged := buf.String()
	assert.Contains(t, logged, "bootstrap_auth_failed", "slog event label must remain stable")
	// The explicit "event" attr mirrors the pre-funnel ssobffBootstrapAuthFailLogger
	// shape so SRE alerts can pivot on either msg or attr until ssobff migrates
	// off the legacy slog-only observer (SSOBFF-BOOTSTRAP-AUDIT-CHAIN-WIRING-01).
	assert.Contains(t, logged, "event=bootstrap_auth_failed", "slog must carry explicit event attr (parity with legacy ssobff shape)")
	assert.Contains(t, logged, "reason=wrong_credentials", "slog must carry reason field")
	assert.Contains(t, logged, "client_ip=192.0.2.99", "slog must carry client_ip field")
	assert.NotContains(t, logged, "bootstrap_audit_append_failed",
		"success path must not emit the fallback line")

	// Audit channel: a hash-chained entry was written to the ledger.
	entries, err := store.Query(context.Background(),
		ledger.AuditFilters{EventType: "bootstrap.auth.fail"},
		query.ListParams{Limit: 10, Sort: ledger.QuerySort})
	require.NoError(t, err)
	require.Len(t, entries, 1, "observer must write exactly one ledger entry per invocation")
	var payload struct {
		Reason   string `json:"reason"`
		ClientIP string `json:"clientIp"`
	}
	require.NoError(t, json.Unmarshal(entries[0].Payload, &payload))
	assert.Equal(t, "wrong_credentials", payload.Reason)
	assert.Equal(t, "192.0.2.99", payload.ClientIP)
}

// TestNewBootstrapAuthFailObserver_AuditAppendFails_LogsFallback covers T5.
// When the ledger Append returns an error, the observer must NOT panic; it
// records a dedicated fallback slog line so the operator sees both the
// original auth failure and the lost audit-chain record.
func TestNewBootstrapAuthFailObserver_AuditAppendFails_LogsFallback(t *testing.T) {
	t.Parallel()
	realStore, clk := buildTestLedgerStore(t)
	store := failingStore{Store: realStore, appendErr: errors.New("ledger boom")}

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	obs, err := audit.NewBootstrapAuthFailObserver(logger, store, clk)
	require.NoError(t, err)

	obs(context.Background(), "rate_limited")

	logged := buf.String()
	// Two slog lines expected: the primary "bootstrap_auth_failed" event
	// (SRE/grep contract) and the dedicated "bootstrap_audit_append_failed"
	// fallback line carrying reason + underlying error. Line count protects
	// against the regression where a future refactor silently swallows the
	// primary line on the audit-fail branch.
	lines := strings.Split(strings.TrimRight(logged, "\n"), "\n")
	assert.Len(t, lines, 2, "exactly two slog lines: primary + fallback; got=%q", logged)
	assert.Contains(t, lines[0], "msg=bootstrap_auth_failed",
		"primary slog line must always be emitted, even when audit append fails")
	assert.Contains(t, lines[1], "msg=bootstrap_audit_append_failed",
		"audit append failure must surface as a dedicated slog Error line")
	assert.Contains(t, lines[1], "event=bootstrap_audit_append_failed",
		"fallback line must carry explicit event attr (parity with primary line)")
	assert.Contains(t, lines[1], "reason=rate_limited", "fallback line must include reason")
	assert.Contains(t, lines[1], "ledger boom", "fallback line must include underlying error text")
}

// TestNewBootstrapAuthFailObserver_DetachedCtxSurvivesCallerCancel covers H1.
// runtime/auth invokes the observer AFTER writing 401/429, so client disconnect
// cancels r.Context() and would otherwise abort the ledger Append mid-flight.
// The observer wraps Append with ctxutil.WithDetachedTimeout so the audit
// hash-chain write completes (up to the detached budget) regardless of caller
// cancellation. This guards the fail-closed compliance contract documented on
// NewBootstrapAuthFailObserver.
func TestNewBootstrapAuthFailObserver_DetachedCtxSurvivesCallerCancel(t *testing.T) {
	t.Parallel()
	store, clk := buildTestLedgerStore(t)

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	obs, err := audit.NewBootstrapAuthFailObserver(logger, store, clk)
	require.NoError(t, err)

	// Pre-canceled parent: simulates the client disconnecting before runtime/auth
	// gets to call the observer. The audit write must still land.
	parent, cancel := context.WithCancel(ctxkeys.WithRealIP(context.Background(), "192.0.2.42"))
	cancel()
	obs(parent, "wrong_credentials")

	entries, err := store.Query(context.Background(),
		ledger.AuditFilters{EventType: "bootstrap.auth.fail"},
		query.ListParams{Limit: 10, Sort: ledger.QuerySort})
	require.NoError(t, err)
	require.Len(t, entries, 1,
		"detached ctx must let the ledger write complete even when the caller ctx is already canceled; "+
			"got %d entries (likely r.Context() leaked into Append)", len(entries))

	// Fallback line must NOT appear — the write succeeded under the detached ctx.
	assert.NotContains(t, buf.String(), "bootstrap_audit_append_failed",
		"detached write should succeed; any 'audit_append_failed' indicates ctx cancel leaked into Append")
}

// TestNewBootstrapAuthFailObserver_NilDeps_Errors covers T6.
// All three dependencies (logger, store, clock) are required wiring; nil or
// typed-nil inputs must be rejected at construction.
func TestNewBootstrapAuthFailObserver_NilDeps_Errors(t *testing.T) {
	t.Parallel()
	store, clk := buildTestLedgerStore(t)

	t.Run("nil logger", func(t *testing.T) {
		t.Parallel()
		_, err := audit.NewBootstrapAuthFailObserver(nil, store, clk)
		require.Error(t, err, "nil logger must be rejected")
	})
	t.Run("nil store", func(t *testing.T) {
		t.Parallel()
		_, err := audit.NewBootstrapAuthFailObserver(slog.Default(), nil, clk)
		require.Error(t, err, "nil store must be rejected")
	})
	t.Run("typed-nil store (concrete pointer wrapped in interface)", func(t *testing.T) {
		t.Parallel()
		// True typed-nil: an interface value carrying type info (*ledger.MemStore)
		// but a nil concrete pointer. A bare `var typedNil ledger.Store` is only
		// a nil interface — distinct from the typed-nil shape, which is what
		// validation.IsNilInterface must catch via reflect.
		var nilMem *ledger.MemStore
		var typedNil ledger.Store = nilMem
		_, err := audit.NewBootstrapAuthFailObserver(slog.Default(), typedNil, clk)
		require.Error(t, err,
			"typed-nil store (concrete-pointer-in-interface) must be rejected via validation.IsNilInterface")
	})
	t.Run("nil clock", func(t *testing.T) {
		t.Parallel()
		_, err := audit.NewBootstrapAuthFailObserver(slog.Default(), store, nil)
		require.Error(t, err, "nil clock must be rejected")
	})
}
