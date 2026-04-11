package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func waitForRelayRunning(t *testing.T, relay *OutboxRelay) {
	t.Helper()

	require.Eventually(t, func() bool {
		return relayState(relay.state.Load()) == relayRunning
	}, time.Second, 5*time.Millisecond)
}

func makeRelayEntry(id, eventType string, attempts int) relayEntry {
	return relayEntry{
		Entry: outbox.Entry{
			ID:            id,
			AggregateID:   "agg-" + id,
			AggregateType: "test",
			EventType:     eventType,
			Payload:       []byte(`{"id":"` + id + `"}`),
			CreatedAt:     time.Now(),
		},
		Attempts: attempts,
	}
}

func makeMockRowData(e relayEntry) mockRowData {
	metaJSON, _ := json.Marshal(e.Metadata)
	if e.Metadata == nil {
		metaJSON = []byte("null")
	}
	return mockRowData{
		values: []any{
			e.ID, e.AggregateID, e.AggregateType, e.EventType,
			e.Topic, e.Payload, metaJSON, e.CreatedAt, e.Attempts,
		},
	}
}

// ---------------------------------------------------------------------------
// Lifecycle tests (updated for relayState enum)
// ---------------------------------------------------------------------------

func TestOutboxRelay_StartStop(t *testing.T) {
	db := &mockDBTX{}
	pub := &mockPublisher{}
	cfg := DefaultRelayConfig()
	cfg.PollInterval = 50 * time.Millisecond

	relay := NewOutboxRelay(db, pub, cfg)

	startCtx, startCancel := context.WithCancel(context.Background())
	defer startCancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- relay.Start(startCtx)
	}()

	waitForRelayRunning(t, relay)

	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()

	err := relay.Stop(stopCtx)
	require.NoError(t, err)

	startErr := <-errCh
	assert.NoError(t, startErr, "Start should return nil on graceful stop per worker.Worker contract")
}

func TestOutboxRelay_StartStop_RaceRegression(t *testing.T) {
	for i := 0; i < 25; i++ {
		db := &mockDBTX{}
		pub := &mockPublisher{}
		cfg := DefaultRelayConfig()
		cfg.PollInterval = 10 * time.Millisecond

		relay := NewOutboxRelay(db, pub, cfg)

		startCtx, startCancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		go func() {
			errCh <- relay.Start(startCtx)
		}()

		waitForRelayRunning(t, relay)

		stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
		err := relay.Stop(stopCtx)
		stopCancel()
		startCancel()

		require.NoErrorf(t, err, "iteration %d", i)
		require.NoErrorf(t, <-errCh, "iteration %d", i)
	}
}

func TestOutboxRelay_CanRestartAfterStop(t *testing.T) {
	db := &mockDBTX{}
	pub := &mockPublisher{}
	cfg := DefaultRelayConfig()
	cfg.PollInterval = 10 * time.Millisecond

	relay := NewOutboxRelay(db, pub, cfg)

	for i := 0; i < 2; i++ {
		startCtx, startCancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		go func() {
			errCh <- relay.Start(startCtx)
		}()

		waitForRelayRunning(t, relay)

		stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
		err := relay.Stop(stopCtx)
		stopCancel()
		startCancel()

		require.NoErrorf(t, err, "iteration %d", i)
		require.NoErrorf(t, <-errCh, "iteration %d", i)
	}
}

func TestOutboxRelay_StopBeforeStart_IsNoop(t *testing.T) {
	db := &mockDBTX{}
	pub := &mockPublisher{}
	relay := NewOutboxRelay(db, pub, DefaultRelayConfig())

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := relay.Stop(ctx)
	assert.NoError(t, err, "Stop on never-started relay must be a no-op")
}

func TestOutboxRelay_DoubleStart_Error(t *testing.T) {
	db := &mockDBTX{}
	pub := &mockPublisher{}
	cfg := DefaultRelayConfig()
	cfg.PollInterval = 50 * time.Millisecond

	relay := NewOutboxRelay(db, pub, cfg)

	startCtx, startCancel := context.WithCancel(context.Background())
	defer startCancel()

	go func() {
		_ = relay.Start(startCtx)
	}()
	waitForRelayRunning(t, relay)

	// Second Start must fail.
	err := relay.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already started")
}

func TestOutboxRelay_ConcurrentStartStop_NoStaleChannel(t *testing.T) {
	db := &mockDBTX{}
	pub := &mockPublisher{}
	cfg := DefaultRelayConfig()
	cfg.PollInterval = 10 * time.Millisecond

	relay := NewOutboxRelay(db, pub, cfg)

	startCtx, startCancel := context.WithCancel(context.Background())
	defer startCancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- relay.Start(startCtx)
	}()

	stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer stopCancel()

	err := relay.Stop(stopCtx)
	assert.NoError(t, err, "Stop must not timeout due to stale channel")

	startCancel()
	<-errCh
}

// ---------------------------------------------------------------------------
// Config tests
// ---------------------------------------------------------------------------

func TestDefaultRelayConfig(t *testing.T) {
	cfg := DefaultRelayConfig()
	assert.Equal(t, 1*time.Second, cfg.PollInterval)
	assert.Equal(t, 100, cfg.BatchSize)
	assert.Equal(t, 72*time.Hour, cfg.RetentionPeriod)
	assert.Equal(t, 5, cfg.MaxAttempts)
	assert.Equal(t, 5*time.Second, cfg.BaseRetryDelay)
	assert.Equal(t, 60*time.Second, cfg.ClaimTTL)
	assert.Equal(t, 5*time.Minute, cfg.MaxRetryDelay)
	assert.Equal(t, 30*time.Second, cfg.ReclaimInterval)
}

func TestNewOutboxRelay_ZeroConfigDefaults(t *testing.T) {
	db := &mockDBTX{}
	pub := &mockPublisher{}
	relay := NewOutboxRelay(db, pub, RelayConfig{})

	defaults := DefaultRelayConfig()
	assert.Equal(t, defaults.PollInterval, relay.config.PollInterval)
	assert.Equal(t, defaults.BatchSize, relay.config.BatchSize)
	assert.Equal(t, defaults.RetentionPeriod, relay.config.RetentionPeriod)
	assert.Equal(t, defaults.MaxAttempts, relay.config.MaxAttempts)
	assert.Equal(t, defaults.BaseRetryDelay, relay.config.BaseRetryDelay)
	assert.Equal(t, defaults.ClaimTTL, relay.config.ClaimTTL)
	assert.Equal(t, defaults.MaxRetryDelay, relay.config.MaxRetryDelay)
	assert.Equal(t, defaults.ReclaimInterval, relay.config.ReclaimInterval)
}

func TestNewOutboxRelay_NegativeConfigDefaults(t *testing.T) {
	db := &mockDBTX{}
	pub := &mockPublisher{}
	relay := NewOutboxRelay(db, pub, RelayConfig{
		PollInterval:    -1 * time.Second,
		BatchSize:       -5,
		RetentionPeriod: -1 * time.Hour,
		MaxAttempts:     -1,
		BaseRetryDelay:  -1 * time.Second,
		ClaimTTL:        -1 * time.Second,
		MaxRetryDelay:   -1 * time.Second,
		ReclaimInterval: -1 * time.Second,
	})

	defaults := DefaultRelayConfig()
	assert.Equal(t, defaults.PollInterval, relay.config.PollInterval)
	assert.Equal(t, defaults.BatchSize, relay.config.BatchSize)
	assert.Equal(t, defaults.RetentionPeriod, relay.config.RetentionPeriod)
	assert.Equal(t, defaults.MaxAttempts, relay.config.MaxAttempts)
}

func TestNewOutboxRelay_ValidConfigPreserved(t *testing.T) {
	db := &mockDBTX{}
	pub := &mockPublisher{}
	custom := RelayConfig{
		PollInterval:    5 * time.Second,
		BatchSize:       50,
		RetentionPeriod: 24 * time.Hour,
		MaxAttempts:     10,
		BaseRetryDelay:  2 * time.Second,
		ClaimTTL:        30 * time.Second,
		MaxRetryDelay:   2 * time.Minute,
		ReclaimInterval: 15 * time.Second,
	}
	relay := NewOutboxRelay(db, pub, custom)

	assert.Equal(t, custom.PollInterval, relay.config.PollInterval)
	assert.Equal(t, custom.BatchSize, relay.config.BatchSize)
	assert.Equal(t, custom.RetentionPeriod, relay.config.RetentionPeriod)
	assert.Equal(t, custom.MaxAttempts, relay.config.MaxAttempts)
}

// ---------------------------------------------------------------------------
// Three-phase tests
// ---------------------------------------------------------------------------

// Test #1: claim → publish all → mark published
func TestRelay_ThreePhase_Success(t *testing.T) {
	e1 := makeRelayEntry("e-1", "order.created", 0)
	e2 := makeRelayEntry("e-2", "order.updated", 0)

	db := &mockDBTX{
		queryRows: &mockRows{entries: []mockRowData{
			makeMockRowData(e1),
			makeMockRowData(e2),
		}},
	}
	pub := &mockPublisher{}
	relay := NewOutboxRelay(db, pub, DefaultRelayConfig())

	err := relay.pollOnce(context.Background())
	require.NoError(t, err)

	// Both entries published.
	require.Len(t, pub.published, 2)

	// writeBack: 2 UPDATEs marking published + optimistic lock.
	publishedExecs := filterExecCalls(db.execCalls, statusPublished)
	require.Len(t, publishedExecs, 2)

	for _, ec := range publishedExecs {
		assert.Contains(t, ec.sql, "status = $3",
			"writeBack UPDATE must include optimistic lock on status (F-8)")
	}
}

// Test #2: partial publish failure
func TestRelay_ThreePhase_PartialFailure(t *testing.T) {
	e1 := makeRelayEntry("e-ok-1", "order.created", 0)
	e2 := makeRelayEntry("e-fail", "order.updated", 0)
	e3 := makeRelayEntry("e-ok-2", "order.deleted", 0)

	db := &mockDBTX{
		queryRows: &mockRows{entries: []mockRowData{
			makeMockRowData(e1),
			makeMockRowData(e2),
			makeMockRowData(e3),
		}},
	}
	pub := &mockPublisher{
		publishErrFunc: func(topic string) error {
			if topic == "order.updated" {
				return errors.New("broker unavailable")
			}
			return nil
		},
	}
	relay := NewOutboxRelay(db, pub, DefaultRelayConfig())

	err := relay.pollOnce(context.Background())
	require.NoError(t, err)

	// 2 published, 1 retried.
	publishedExecs := filterExecCalls(db.execCalls, statusPublished)
	assert.Len(t, publishedExecs, 2)

	retryExecs := filterExecCalls(db.execCalls, statusPending)
	require.Len(t, retryExecs, 1)
	// Verify last_error is written.
	assert.Contains(t, fmt.Sprintf("%v", retryExecs[0].args), "broker unavailable")
}

// Test #3: exponential backoff with jitter, capped by MaxRetryDelay
func TestRelay_ExponentialBackoff_WithJitter(t *testing.T) {
	db := &mockDBTX{}
	pub := &mockPublisher{}
	cfg := DefaultRelayConfig()
	cfg.BaseRetryDelay = 1 * time.Second
	cfg.MaxRetryDelay = 10 * time.Second
	relay := NewOutboxRelay(db, pub, cfg)

	// Test retryDelay for various attempt counts.
	for attempts := 1; attempts <= 8; attempts++ {
		delay := relay.retryDelay(attempts)
		expectedBase := relay.cappedDelay(cfg.BaseRetryDelay * (1 << attempts))
		maxJitter := expectedBase / 4

		assert.GreaterOrEqual(t, delay, expectedBase,
			"delay must be >= base for attempts=%d", attempts)
		assert.LessOrEqual(t, delay, expectedBase+maxJitter,
			"delay must be <= base+jitter for attempts=%d", attempts)
		assert.LessOrEqual(t, delay, cfg.MaxRetryDelay+cfg.MaxRetryDelay/4,
			"delay must be capped near MaxRetryDelay for attempts=%d", attempts)
	}
}

// Test #4: attempts >= MaxAttempts → dead
func TestRelay_MaxAttempts_DeadLetter(t *testing.T) {
	// Entry already at attempts=4, MaxAttempts=5. Next failure → dead.
	e := makeRelayEntry("e-dying", "audit.event", 4)

	db := &mockDBTX{
		queryRows: &mockRows{entries: []mockRowData{makeMockRowData(e)}},
	}
	pub := &mockPublisher{publishErr: errors.New("permanent failure")}
	cfg := DefaultRelayConfig()
	cfg.MaxAttempts = 5
	relay := NewOutboxRelay(db, pub, cfg)

	err := relay.pollOnce(context.Background())
	require.NoError(t, err)

	deadExecs := filterExecCalls(db.execCalls, statusDead)
	require.Len(t, deadExecs, 1)
	assert.Contains(t, fmt.Sprintf("%v", deadExecs[0].args), "permanent failure")
}

// Test #5: reclaimStale increments attempts (F-4)
func TestRelay_ReclaimStale_IncrementsAttempts(t *testing.T) {
	db := &mockDBTX{}
	cfg := DefaultRelayConfig()
	relay := NewOutboxRelay(db, &mockPublisher{}, cfg)

	err := relay.reclaimStale(context.Background())
	require.NoError(t, err)

	// Verify the SQL increments attempts.
	require.Len(t, db.execCalls, 1)
	sql := db.execCalls[0].sql
	assert.Contains(t, sql, "attempts = attempts + 1",
		"reclaimStale must increment attempts (F-4)")
	// statusDead is passed as $3 parameter, verify in args.
	args := db.execCalls[0].args
	assert.Contains(t, args, statusDead,
		"reclaimStale must pass dead status for transition when attempts >= max")
}

// Test #6: reclaimStale with attempts+1 >= max → dead (F-4 boundary)
func TestRelay_ReclaimStale_MaxAttempts_Dead(t *testing.T) {
	db := &mockDBTX{}
	cfg := DefaultRelayConfig()
	cfg.MaxAttempts = 3
	relay := NewOutboxRelay(db, &mockPublisher{}, cfg)

	err := relay.reclaimStale(context.Background())
	require.NoError(t, err)

	require.Len(t, db.execCalls, 1)
	args := db.execCalls[0].args
	// $2 = MaxAttempts = 3, $3 = statusDead, $4 = statusPending
	assert.Equal(t, 3, args[1], "MaxAttempts passed to reclaimStale SQL")
	assert.Equal(t, statusDead, args[2], "dead status passed to reclaimStale SQL")
	assert.Equal(t, statusPending, args[3], "pending status passed to reclaimStale SQL")
}

// Test #7: claim SQL uses FOR UPDATE SKIP LOCKED
func TestRelay_ConcurrentRelays_SkipLocked(t *testing.T) {
	db := &mockDBTX{
		queryRows: &mockRows{entries: nil},
	}
	pub := &mockPublisher{}
	relay := NewOutboxRelay(db, pub, DefaultRelayConfig())

	_, err := relay.claim(context.Background())
	require.NoError(t, err)

	// Verify claim SQL contains SKIP LOCKED.
	require.NotEmpty(t, db.queryCalls)
	claimSQL := db.queryCalls[0].sql
	assert.Contains(t, claimSQL, "FOR UPDATE SKIP LOCKED",
		"claim must use FOR UPDATE SKIP LOCKED for multi-instance safety")
	// statusClaiming is passed as $1 parameter, verify in args.
	claimArgs := db.queryCalls[0].args
	assert.Contains(t, claimArgs, statusClaiming,
		"claim must pass claiming status as parameter")
}

// Test #8: writeBack optimistic lock — entry already reclaimed → skipped (F-8)
func TestRelay_WriteBack_OptimisticLock_Skip(t *testing.T) {
	e := makeRelayEntry("e-reclaimed", "order.created", 0)

	// Mock DB returns 0 affected rows (entry was reclaimed by reclaimStale).
	db := &mockDBTX{
		execResult: pgconn.NewCommandTag("UPDATE 0"),
	}
	pub := &mockPublisher{}
	relay := NewOutboxRelay(db, pub, DefaultRelayConfig())

	results := []publishResult{
		{entry: e, err: nil}, // publish succeeded, but entry was reclaimed
	}

	stats, err := relay.writeBack(context.Background(), results)
	require.NoError(t, err)

	assert.Equal(t, 0, stats.published, "should not count as published")
	assert.Equal(t, 1, stats.skipped, "should count as skipped (F-8)")
}

// Test #9: pending entries with future next_retry_at are skipped (S3-F1)
func TestRelay_PendingSkipped_ByRetryAt(t *testing.T) {
	// No entries returned by claim (all have future next_retry_at).
	db := &mockDBTX{
		queryRows: &mockRows{entries: nil},
	}
	pub := &mockPublisher{}
	relay := NewOutboxRelay(db, pub, DefaultRelayConfig())

	err := relay.pollOnce(context.Background())
	require.NoError(t, err)

	// No publish calls.
	assert.Empty(t, pub.published)

	// Verify claim SQL filters by next_retry_at.
	require.NotEmpty(t, db.queryCalls)
	claimSQL := db.queryCalls[0].sql
	assert.Contains(t, claimSQL, "next_retry_at IS NULL OR next_retry_at <= now()",
		"claim must filter out entries with future next_retry_at (S3-F1)")
}

// Test #10: writeBack commit failure → rollback (S3-F2)
func TestRelay_WriteBack_CommitFailure_Rollback(t *testing.T) {
	e := makeRelayEntry("e-commit-fail", "order.created", 0)

	db := &mockDBTX{
		commitErr: errors.New("commit failed"),
	}
	pub := &mockPublisher{}
	relay := NewOutboxRelay(db, pub, DefaultRelayConfig())

	results := []publishResult{
		{entry: e, err: nil},
	}

	_, err := relay.writeBack(context.Background(), results)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "commit")
}

// ---------------------------------------------------------------------------
// Cleanup tests
// ---------------------------------------------------------------------------

func TestOutboxRelay_DeletePublishedBefore(t *testing.T) {
	db := &mockDBTX{}
	pub := &mockPublisher{}
	relay := NewOutboxRelay(db, pub, DefaultRelayConfig())

	cutoff := time.Now().Add(-72 * time.Hour)
	err := relay.deletePublishedBefore(context.Background(), cutoff)
	require.NoError(t, err)

	// Should have 2 exec calls: one for published, one for dead.
	require.Len(t, db.execCalls, 2)
	// First call: delete published entries by status (parameter $1) and published_at.
	assert.Contains(t, db.execCalls[0].args, statusPublished)
	assert.Contains(t, db.execCalls[0].sql, "published_at")
	// Second call: delete dead entries by status (parameter $1).
	assert.Contains(t, db.execCalls[1].args, statusDead)
}

// ---------------------------------------------------------------------------
// Wire format test
// ---------------------------------------------------------------------------

func TestOutboxMessage_JSONFormat(t *testing.T) {
	msg := outboxMessage{
		ID:        "test-id",
		EventType: "order.created",
		Topic:     "orders",
		Payload:   json.RawMessage(`{"key":"value"}`),
		Metadata:  map[string]string{"k": "v"},
		CreatedAt: time.Date(2026, 4, 11, 0, 0, 0, 0, time.UTC),
	}

	data, err := json.Marshal(msg)
	require.NoError(t, err)

	// Verify camelCase JSON keys (S1-F1).
	var m map[string]any
	require.NoError(t, json.Unmarshal(data, &m))
	assert.Contains(t, m, "id")
	assert.Contains(t, m, "eventType")
	assert.Contains(t, m, "topic")
	assert.Contains(t, m, "payload")
	assert.Contains(t, m, "metadata")
	assert.Contains(t, m, "createdAt")

	// Must NOT contain PascalCase or internal fields.
	assert.NotContains(t, m, "EventType")
	assert.NotContains(t, m, "Attempts")
}

// ---------------------------------------------------------------------------
// Helper
// ---------------------------------------------------------------------------

func filterExecCalls(calls []execCall, statusValue string) []execCall {
	var result []execCall
	for _, c := range calls {
		for _, arg := range c.args {
			if s, ok := arg.(string); ok && s == statusValue {
				result = append(result, c)
				break
			}
		}
	}
	return result
}

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

type mockDBTX struct {
	mu         sync.Mutex
	queryRows  *mockRows
	queryCalls []queryCall
	execCalls  []execCall
	execErr    error
	execResult pgconn.CommandTag
	commitErr  error
}

type queryCall struct {
	sql  string
	args []any
}

func (m *mockDBTX) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.execCalls = append(m.execCalls, execCall{sql: sql, args: args})
	if m.execErr != nil {
		return pgconn.NewCommandTag(""), m.execErr
	}
	if m.execResult.String() != "" {
		return m.execResult, nil
	}
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

func (m *mockDBTX) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.queryCalls = append(m.queryCalls, queryCall{sql: sql, args: args})
	if m.queryRows == nil {
		return &mockRows{}, nil
	}
	return m.queryRows, nil
}

func (m *mockDBTX) Begin(_ context.Context) (pgx.Tx, error) {
	return &mockRelayTx{db: m}, nil
}

type mockRelayTx struct {
	db *mockDBTX
}

func (t *mockRelayTx) Begin(_ context.Context) (pgx.Tx, error) { return t, nil }
func (t *mockRelayTx) Commit(_ context.Context) error {
	if t.db.commitErr != nil {
		return t.db.commitErr
	}
	return nil
}
func (t *mockRelayTx) Rollback(_ context.Context) error { return nil }
func (t *mockRelayTx) CopyFrom(_ context.Context, _ pgx.Identifier, _ []string, _ pgx.CopyFromSource) (int64, error) {
	return 0, nil
}
func (t *mockRelayTx) SendBatch(_ context.Context, _ *pgx.Batch) pgx.BatchResults { return nil }
func (t *mockRelayTx) LargeObjects() pgx.LargeObjects                             { return pgx.LargeObjects{} }
func (t *mockRelayTx) Prepare(_ context.Context, _ string, _ string) (*pgconn.StatementDescription, error) {
	return nil, nil
}
func (t *mockRelayTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return t.db.Exec(ctx, sql, args...)
}
func (t *mockRelayTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	return t.db.Query(ctx, sql, args...)
}
func (t *mockRelayTx) QueryRow(_ context.Context, _ string, _ ...any) pgx.Row { return nil }
func (t *mockRelayTx) Conn() *pgx.Conn                                        { return nil }

type mockRowData struct {
	values []any
}

type mockRows struct {
	entries []mockRowData
	idx     int
}

func (r *mockRows) Next() bool {
	return r.idx < len(r.entries)
}

func (r *mockRows) Scan(dest ...any) error {
	row := r.entries[r.idx]
	r.idx++
	if len(dest) != len(row.values) {
		return fmt.Errorf("mockRows.Scan: dest count %d != values count %d (S3-F4)", len(dest), len(row.values))
	}
	for i, v := range row.values {
		switch d := dest[i].(type) {
		case *string:
			*d = v.(string)
		case *[]byte:
			*d = v.([]byte)
		case *time.Time:
			*d = v.(time.Time)
		case *int:
			*d = v.(int)
		}
	}
	return nil
}

func (r *mockRows) Close()                                       {}
func (r *mockRows) Err() error                                   { return nil }
func (r *mockRows) CommandTag() pgconn.CommandTag                { return pgconn.NewCommandTag("") }
func (r *mockRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *mockRows) Values() ([]any, error)                       { return nil, nil }
func (r *mockRows) RawValues() [][]byte                          { return nil }
func (r *mockRows) Conn() *pgx.Conn                              { return nil }

type publishCall struct {
	topic   string
	payload []byte
}

type mockPublisher struct {
	mu             sync.Mutex
	published      []publishCall
	publishErr     error
	publishErrFunc func(topic string) error
}

func (p *mockPublisher) Publish(_ context.Context, topic string, payload []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.publishErrFunc != nil {
		if err := p.publishErrFunc(topic); err != nil {
			return err
		}
	} else if p.publishErr != nil {
		return p.publishErr
	}
	p.published = append(p.published, publishCall{topic: topic, payload: payload})
	return nil
}
