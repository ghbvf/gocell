package redis

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// mockCmdable implements the cmdable interface for unit testing.
// It stores values in an in-memory map and simulates Redis behaviour
// including SET NX, TTL expiry, GET, DEL, Eval, Ping, and Close.
type mockCmdable struct {
	mu     sync.Mutex
	store  map[string]mockEntry
	closed bool

	// Override hooks for injecting errors in tests.
	pingErr  error
	closeErr error
	setErr   error
	getErr   error
	delErr   error
	setNXErr error
	evalErr  error

	// evalRenewResult, when non-nil, overrides the return value for the
	// renew Lua script (2-arg Eval). Set to pointer-to-zero to simulate
	// ownership loss (another holder took over).
	evalRenewResult *int64

	// evalCallCount counts the total number of Eval invocations.
	// Used by tests that assert DEL was issued regardless of caller ctx state.
	evalCallCount int
}

type mockEntry struct {
	value  string
	expiry time.Time // zero means no expiry
}

func newMockCmdable() *mockCmdable {
	return &mockCmdable{
		store: make(map[string]mockEntry),
	}
}

func (m *mockCmdable) Ping(_ context.Context) *goredis.StatusCmd {
	cmd := goredis.NewStatusCmd(context.Background())
	if m.pingErr != nil {
		cmd.SetErr(m.pingErr)
	} else {
		cmd.SetVal("PONG")
	}
	return cmd
}

func (m *mockCmdable) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return m.closeErr
}

func (m *mockCmdable) Set(_ context.Context, key string, value any, expiration time.Duration) *goredis.StatusCmd {
	cmd := goredis.NewStatusCmd(context.Background())
	if m.setErr != nil {
		cmd.SetErr(m.setErr)
		return cmd
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := mockEntry{value: toString(value)}
	if expiration > 0 {
		entry.expiry = time.Now().Add(expiration)
	}
	m.store[key] = entry
	cmd.SetVal("OK")
	return cmd
}

func (m *mockCmdable) Get(_ context.Context, key string) *goredis.StringCmd {
	cmd := goredis.NewStringCmd(context.Background())
	if m.getErr != nil {
		cmd.SetErr(m.getErr)
		return cmd
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.store[key]
	if !ok {
		cmd.SetErr(goredis.Nil)
		return cmd
	}
	if !entry.expiry.IsZero() && time.Now().After(entry.expiry) {
		delete(m.store, key)
		cmd.SetErr(goredis.Nil)
		return cmd
	}
	cmd.SetVal(entry.value)
	return cmd
}

func (m *mockCmdable) Del(_ context.Context, keys ...string) *goredis.IntCmd {
	cmd := goredis.NewIntCmd(context.Background())
	if m.delErr != nil {
		cmd.SetErr(m.delErr)
		return cmd
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var count int64
	for _, key := range keys {
		if _, ok := m.store[key]; ok {
			delete(m.store, key)
			count++
		}
	}
	cmd.SetVal(count)
	return cmd
}

func (m *mockCmdable) SetNX(_ context.Context, key string, value any, expiration time.Duration) *goredis.BoolCmd {
	cmd := goredis.NewBoolCmd(context.Background())
	if m.setNXErr != nil {
		cmd.SetErr(m.setNXErr)
		return cmd
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if key already exists and not expired.
	if entry, ok := m.store[key]; ok {
		if entry.expiry.IsZero() || time.Now().Before(entry.expiry) {
			cmd.SetVal(false)
			return cmd
		}
		// Expired, treat as not existing.
		delete(m.store, key)
	}

	entry := mockEntry{value: toString(value)}
	if expiration > 0 {
		entry.expiry = time.Now().Add(expiration)
	}
	m.store[key] = entry
	cmd.SetVal(true)
	return cmd
}

func (m *mockCmdable) Eval(_ context.Context, script string, keys []string, args ...any) *goredis.Cmd {
	cmd := goredis.NewCmd(context.Background())
	m.mu.Lock()
	defer m.mu.Unlock()
	m.evalCallCount++
	if m.evalErr != nil {
		cmd.SetErr(m.evalErr)
		return cmd
	}

	// Simulate the release lock script: GET key == value → DEL → 1, else → 0.
	// Also simulate the renew lock script: GET key == value → PEXPIRE → 1, else → 0.
	if len(keys) == 1 && len(args) >= 1 {
		key := keys[0]
		expectedValue := toString(args[0])
		entry, ok := m.store[key]
		if ok && entry.value == expectedValue {
			// Distinguish between release (1 arg) and renew (2 args).
			if len(args) == 1 {
				// Release: delete the key.
				delete(m.store, key)
				cmd.SetVal(int64(1))
			} else {
				// Renew: allow override for ownership-loss simulation.
				if m.evalRenewResult != nil {
					cmd.SetVal(*m.evalRenewResult)
					return cmd
				}
				ttlMs, _ := toInt64(args[1])
				entry.expiry = time.Now().Add(time.Duration(ttlMs) * time.Millisecond)
				m.store[key] = entry
				cmd.SetVal(int64(1))
			}
		} else {
			cmd.SetVal(int64(0))
		}
	} else {
		cmd.SetVal(int64(0))
	}
	return cmd
}

// toString converts various types to string for mock storage.
func toString(v any) string {
	switch val := v.(type) {
	case string:
		return val
	case []byte:
		return string(val)
	default:
		return ""
	}
}

// toInt64 converts various numeric types to int64.
func toInt64(v any) (int64, bool) {
	switch val := v.(type) {
	case int64:
		return val, true
	case int:
		return int64(val), true
	case float64:
		return int64(val), true
	default:
		return 0, false
	}
}

// errMock is a sentinel error used in tests.
var errMock = errors.New("mock error")

// recordingCmdable wraps mockCmdable and records the context passed to each
// Eval call. Used by deadline-shape tests that need to inspect the deadline
// the production code computes.
type recordingCmdable struct {
	*mockCmdable
	mu           sync.Mutex
	evalContexts []context.Context
}

func newRecordingCmdable() *recordingCmdable {
	return &recordingCmdable{
		mockCmdable: newMockCmdable(),
	}
}

func (r *recordingCmdable) Eval(ctx context.Context, script string, keys []string, args ...any) *goredis.Cmd {
	r.mu.Lock()
	r.evalContexts = append(r.evalContexts, ctx)
	r.mu.Unlock()
	return r.mockCmdable.Eval(ctx, script, keys, args...)
}

func (r *recordingCmdable) lastEvalCtx() context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.evalContexts) == 0 {
		return nil
	}
	return r.evalContexts[len(r.evalContexts)-1]
}

// mockPoolStatsProvider implements poolStatsProvider for testing.
type mockPoolStatsProvider struct {
	stats *goredis.PoolStats
}

func (m *mockPoolStatsProvider) PoolStats() *goredis.PoolStats {
	return m.stats
}

// claimerMockCmdable extends mockCmdable with Eval behavior that simulates
// the IdempotencyClaimer's Lua scripts (claim, commit, release).
type claimerMockCmdable struct {
	mockCmdable
}

func newClaimerMock() *claimerMockCmdable {
	return &claimerMockCmdable{
		mockCmdable: mockCmdable{
			store: make(map[string]mockEntry),
		},
	}
}

// Eval overrides the base mock to simulate the claimer Lua scripts.
// Distinguishes claim vs commit by key order:
//   - Claim:   keys=[done:{k}, lease:{k}]  (keys[0] starts with "done:")
//   - Commit:  keys=[lease:{k}, done:{k}]  (keys[0] starts with "lease:")
//   - Release: keys=[lease:{k}]            (single key)
func (m *claimerMockCmdable) Eval(_ context.Context, _ string, keys []string, args ...any) *goredis.Cmd {
	cmd := goredis.NewCmd(context.Background())
	if m.evalErr != nil {
		cmd.SetErr(m.evalErr)
		return cmd
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	switch {
	// Release script: 1 key (leaseKey), 1 arg (token)
	case len(keys) == 1 && len(args) == 1:
		leaseKey := keys[0]
		token := toString(args[0])
		if entry, ok := m.store[leaseKey]; ok && entry.value == token {
			delete(m.store, leaseKey)
			cmd.SetVal(int64(1))
		} else {
			cmd.SetVal(int64(0))
		}
		return cmd

	// Claim script: 2 keys, keys[0] starts with "done:"
	case len(keys) == 2 && len(args) >= 2 && strings.HasPrefix(keys[0], "done:"):
		doneKey, leaseKey := keys[0], keys[1]
		token := toString(args[0])
		leaseSec, _ := toInt64(args[1])

		if _, ok := m.store[doneKey]; ok {
			cmd.SetVal(int64(0)) // ClaimDone
			return cmd
		}
		if entry, ok := m.store[leaseKey]; ok {
			if entry.expiry.IsZero() || time.Now().Before(entry.expiry) {
				cmd.SetVal(int64(2)) // ClaimBusy
				return cmd
			}
			delete(m.store, leaseKey) // expired
		}
		m.store[leaseKey] = mockEntry{
			value:  token,
			expiry: time.Now().Add(time.Duration(leaseSec) * time.Second),
		}
		cmd.SetVal(int64(1)) // ClaimAcquired
		return cmd

	// Commit script: 2 keys, keys[0] starts with "lease:"
	case len(keys) == 2 && len(args) == 2 && strings.HasPrefix(keys[0], "lease:"):
		leaseKey, doneKey := keys[0], keys[1]
		token := toString(args[0])
		doneSec, _ := toInt64(args[1])

		if entry, ok := m.store[leaseKey]; ok && entry.value == token {
			delete(m.store, leaseKey)
			m.store[doneKey] = mockEntry{
				value:  "1",
				expiry: time.Now().Add(time.Duration(doneSec) * time.Second),
			}
			cmd.SetVal(int64(1))
		} else {
			cmd.SetVal(int64(0))
		}
		return cmd

	default:
		cmd.SetVal(int64(0))
		return cmd
	}
}
