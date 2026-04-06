package redis

import (
	"context"
	"errors"
	"sync"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// mockCmdable implements the cmdable interface for unit testing.
// It stores values in an in-memory map and simulates Redis behaviour
// including SET NX, TTL expiry, GET, DEL, Eval, Ping, and Close.
type mockCmdable struct {
	mu            sync.Mutex
	store         map[string]mockEntry
	fenceCounters map[string]int64
	closed        bool

	// Override hooks for injecting errors in tests.
	pingErr  error
	closeErr error
	setErr   error
	getErr   error
	delErr   error
	setNXErr error
	evalErr  error
}

type mockEntry struct {
	value  string
	expiry time.Time // zero means no expiry
}

func newMockCmdable() *mockCmdable {
	return &mockCmdable{
		store:         make(map[string]mockEntry),
		fenceCounters: make(map[string]int64),
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
	if m.evalErr != nil {
		cmd.SetErr(m.evalErr)
		return cmd
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	// Simulate fencing token script: 2 keys (lock + fence) + 1 arg (value).
	// Checks ownership (GET KEYS[1] == ARGV[1]) then INCR KEYS[2].
	if len(keys) == 2 && len(args) == 1 {
		lockKey := keys[0]
		fenceKey := keys[1]
		expectedValue := toString(args[0])
		entry, ok := m.store[lockKey]
		if ok && entry.value == expectedValue {
			m.fenceCounters[fenceKey]++
			cmd.SetVal(m.fenceCounters[fenceKey])
		} else {
			cmd.SetVal(int64(0))
		}
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
			} else {
				// Renew: update expiry.
				ttlMs, _ := toInt64(args[1])
				entry.expiry = time.Now().Add(time.Duration(ttlMs) * time.Millisecond)
				m.store[key] = entry
			}
			cmd.SetVal(int64(1))
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
