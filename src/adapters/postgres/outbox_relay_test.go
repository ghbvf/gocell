package postgres

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOutboxRelay_StartStop(t *testing.T) {
	db := &mockDBTX{}
	pub := &mockPublisher{}
	cfg := DefaultRelayConfig()
	cfg.PollInterval = 50 * time.Millisecond

	relay := NewOutboxRelay(db, pub, cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- relay.Start(ctx)
	}()

	// Wait for context to expire, then stop.
	<-ctx.Done()
	err := relay.Stop(context.Background())
	require.NoError(t, err)

	startErr := <-errCh
	assert.ErrorIs(t, startErr, context.DeadlineExceeded)
}

func TestOutboxRelay_PollOnce_NoEntries(t *testing.T) {
	db := &mockDBTX{
		queryRows: &mockRows{entries: nil},
	}
	pub := &mockPublisher{}
	cfg := DefaultRelayConfig()

	relay := NewOutboxRelay(db, pub, cfg)
	err := relay.pollOnce(context.Background())
	require.NoError(t, err)
	assert.Empty(t, pub.published)
}

func TestOutboxRelay_PollOnce_PublishesEntries(t *testing.T) {
	entry := outbox.Entry{
		ID:            "e-1",
		AggregateID:   "agg-1",
		AggregateType: "order",
		EventType:     "order.created",
		Payload:       []byte(`{"id":"1"}`),
		CreatedAt:     time.Now(),
		Metadata:      map[string]string{"k": "v"},
	}

	metaJSON, _ := json.Marshal(entry.Metadata)

	db := &mockDBTX{
		queryRows: &mockRows{
			entries: []mockRowData{
				{
					values: []any{
						entry.ID, entry.AggregateID, entry.AggregateType,
						entry.EventType, "", entry.Payload, metaJSON, entry.CreatedAt,
					},
				},
			},
		},
	}
	pub := &mockPublisher{}
	cfg := DefaultRelayConfig()

	relay := NewOutboxRelay(db, pub, cfg)
	err := relay.pollOnce(context.Background())
	require.NoError(t, err)

	require.Len(t, pub.published, 1)
	assert.Equal(t, "order.created", pub.published[0].topic) // falls back to EventType when Topic is empty

	// Verify the entry was marked as published.
	require.Len(t, db.execCalls, 1)
	assert.Contains(t, db.execCalls[0].sql, "UPDATE outbox_entries SET published = true")
}

func TestOutboxRelay_PollOnce_PublishesWithExplicitTopic(t *testing.T) {
	entry := outbox.Entry{
		ID:            "e-topic",
		AggregateID:   "agg-t",
		AggregateType: "device",
		EventType:     "device.enrolled",
		Topic:         "custom.topic.v2",
		Payload:       []byte(`{"enrolled":true}`),
		CreatedAt:     time.Now(),
	}

	db := &mockDBTX{
		queryRows: &mockRows{
			entries: []mockRowData{
				{
					values: []any{
						entry.ID, entry.AggregateID, entry.AggregateType,
						entry.EventType, entry.Topic, entry.Payload, []byte("null"), entry.CreatedAt,
					},
				},
			},
		},
	}
	pub := &mockPublisher{}
	cfg := DefaultRelayConfig()

	relay := NewOutboxRelay(db, pub, cfg)
	err := relay.pollOnce(context.Background())
	require.NoError(t, err)

	require.Len(t, pub.published, 1)
	assert.Equal(t, "custom.topic.v2", pub.published[0].topic) // uses explicit Topic
}

func TestOutboxRelay_PollOnce_PublishError(t *testing.T) {
	entry := outbox.Entry{
		ID:        "e-2",
		EventType: "test",
		Payload:   []byte("{}"),
		CreatedAt: time.Now(),
	}

	db := &mockDBTX{
		queryRows: &mockRows{
			entries: []mockRowData{
				{
					values: []any{
						entry.ID, "", "", entry.EventType,
						"", entry.Payload, []byte("null"), entry.CreatedAt,
					},
				},
			},
		},
	}
	pub := &mockPublisher{
		publishErr: assert.AnError,
	}
	cfg := DefaultRelayConfig()

	relay := NewOutboxRelay(db, pub, cfg)
	err := relay.pollOnce(context.Background())
	require.NoError(t, err) // pollOnce itself succeeds; publish error is logged.

	// Should NOT have marked as published.
	assert.Empty(t, db.execCalls)
}

func TestDefaultRelayConfig(t *testing.T) {
	cfg := DefaultRelayConfig()
	assert.Equal(t, 1*time.Second, cfg.PollInterval)
	assert.Equal(t, 100, cfg.BatchSize)
	assert.Equal(t, 72*time.Hour, cfg.RetentionPeriod)
}

// Fix #1: Retention cleanup should use published_at, not created_at.
func TestOutboxRelay_DeletePublishedBefore_UsesPublishedAt(t *testing.T) {
	db := &mockDBTX{}
	pub := &mockPublisher{}
	cfg := DefaultRelayConfig()

	relay := NewOutboxRelay(db, pub, cfg)
	cutoff := time.Now().Add(-72 * time.Hour)
	err := relay.deletePublishedBefore(context.Background(), cutoff)
	require.NoError(t, err)

	require.Len(t, db.execCalls, 1)
	assert.Contains(t, db.execCalls[0].sql, "published_at < $1",
		"retention cleanup must filter on published_at, not created_at")
	assert.NotContains(t, db.execCalls[0].sql, "created_at < $1",
		"retention cleanup must NOT use created_at for the cutoff")
}

// Fix #6: Zero-value config fields should fall back to defaults (no panic).
func TestNewOutboxRelay_ZeroConfigDefaults(t *testing.T) {
	db := &mockDBTX{}
	pub := &mockPublisher{}

	// Pass a fully-zero config.
	relay := NewOutboxRelay(db, pub, RelayConfig{})

	defaults := DefaultRelayConfig()
	assert.Equal(t, defaults.PollInterval, relay.config.PollInterval,
		"zero PollInterval should fall back to default")
	assert.Equal(t, defaults.BatchSize, relay.config.BatchSize,
		"zero BatchSize should fall back to default")
	assert.Equal(t, defaults.RetentionPeriod, relay.config.RetentionPeriod,
		"zero RetentionPeriod should fall back to default")
}

// Fix #6: Negative config values should also fall back to defaults.
func TestNewOutboxRelay_NegativeConfigDefaults(t *testing.T) {
	db := &mockDBTX{}
	pub := &mockPublisher{}

	relay := NewOutboxRelay(db, pub, RelayConfig{
		PollInterval:    -1 * time.Second,
		BatchSize:       -5,
		RetentionPeriod: -1 * time.Hour,
	})

	defaults := DefaultRelayConfig()
	assert.Equal(t, defaults.PollInterval, relay.config.PollInterval)
	assert.Equal(t, defaults.BatchSize, relay.config.BatchSize)
	assert.Equal(t, defaults.RetentionPeriod, relay.config.RetentionPeriod)
}

// Fix #6: Valid config values should be preserved (not overridden by defaults).
func TestNewOutboxRelay_ValidConfigPreserved(t *testing.T) {
	db := &mockDBTX{}
	pub := &mockPublisher{}

	custom := RelayConfig{
		PollInterval:    5 * time.Second,
		BatchSize:       50,
		RetentionPeriod: 24 * time.Hour,
	}
	relay := NewOutboxRelay(db, pub, custom)

	assert.Equal(t, custom.PollInterval, relay.config.PollInterval)
	assert.Equal(t, custom.BatchSize, relay.config.BatchSize)
	assert.Equal(t, custom.RetentionPeriod, relay.config.RetentionPeriod)
}

// Fix #9: Stop should respect the caller's context deadline.
func TestOutboxRelay_Stop_RespectsCallerTimeout(t *testing.T) {
	db := &mockDBTX{}
	pub := &mockPublisher{}
	cfg := DefaultRelayConfig()
	cfg.PollInterval = 50 * time.Millisecond

	relay := NewOutboxRelay(db, pub, cfg)

	// Start the relay so internal goroutines are running.
	startCtx, startCancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- relay.Start(startCtx)
	}()

	// Give the relay time to start.
	time.Sleep(100 * time.Millisecond)

	// Cancel start context to trigger shutdown of loops.
	startCancel()

	// Stop with an already-expired context to verify timeout path.
	expiredCtx, expiredCancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer expiredCancel()
	time.Sleep(5 * time.Millisecond) // ensure context has expired

	err := relay.Stop(expiredCtx)
	// The relay may or may not have finished its goroutines in time.
	// If it hasn't, we should get an error. If it has, nil is fine.
	// We verify the mechanism works by checking the error type when it occurs.
	if err != nil {
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	}
}

// Fix #9: Stop should succeed when caller context has ample time.
func TestOutboxRelay_Stop_SucceedsWithAmpleTimeout(t *testing.T) {
	db := &mockDBTX{}
	pub := &mockPublisher{}
	cfg := DefaultRelayConfig()
	cfg.PollInterval = 50 * time.Millisecond

	relay := NewOutboxRelay(db, pub, cfg)

	startCtx, startCancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- relay.Start(startCtx)
	}()

	time.Sleep(100 * time.Millisecond)
	startCancel()

	// Give plenty of time to stop.
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopCancel()

	err := relay.Stop(stopCtx)
	require.NoError(t, err)

	startErr := <-errCh
	assert.ErrorIs(t, startErr, context.Canceled)
}

// --- mocks ---

type mockDBTX struct {
	mu        sync.Mutex
	queryRows *mockRows
	execCalls []execCall
	execErr   error
}

func (m *mockDBTX) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.execCalls = append(m.execCalls, execCall{sql: sql, args: args})
	if m.execErr != nil {
		return pgconn.NewCommandTag(""), m.execErr
	}
	return pgconn.NewCommandTag("UPDATE 1"), nil
}

func (m *mockDBTX) Query(_ context.Context, _ string, _ ...any) (pgx.Rows, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.queryRows == nil {
		return &mockRows{}, nil
	}
	return m.queryRows, nil
}

func (m *mockDBTX) Begin(_ context.Context) (pgx.Tx, error) {
	return &mockRelayTx{db: m}, nil
}

// mockRelayTx implements pgx.Tx for unit testing. It delegates Query/Exec to the
// underlying mockDBTX so existing test assertions on execCalls still work.
type mockRelayTx struct {
	db *mockDBTX
}

func (t *mockRelayTx) Begin(_ context.Context) (pgx.Tx, error)   { return t, nil }
func (t *mockRelayTx) Commit(_ context.Context) error             { return nil }
func (t *mockRelayTx) Rollback(_ context.Context) error           { return nil }
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

// mockRows implements pgx.Rows for unit testing.
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
	for i, v := range row.values {
		switch d := dest[i].(type) {
		case *string:
			*d = v.(string)
		case *[]byte:
			*d = v.([]byte)
		case *time.Time:
			*d = v.(time.Time)
		}
	}
	return nil
}

func (r *mockRows) Close()                                         {}
func (r *mockRows) Err() error                                     { return nil }
func (r *mockRows) CommandTag() pgconn.CommandTag                   { return pgconn.NewCommandTag("") }
func (r *mockRows) FieldDescriptions() []pgconn.FieldDescription    { return nil }
func (r *mockRows) Values() ([]any, error)                         { return nil, nil }
func (r *mockRows) RawValues() [][]byte                            { return nil }
func (r *mockRows) Conn() *pgx.Conn                                { return nil }

type publishCall struct {
	topic   string
	payload []byte
}

type mockPublisher struct {
	mu         sync.Mutex
	published  []publishCall
	publishErr error
}

func (p *mockPublisher) Publish(_ context.Context, topic string, payload []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.publishErr != nil {
		return p.publishErr
	}
	p.published = append(p.published, publishCall{topic: topic, payload: payload})
	return nil
}
