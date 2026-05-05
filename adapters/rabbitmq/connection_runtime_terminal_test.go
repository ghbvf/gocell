package rabbitmq

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// TestReconnectWithBackoff_PermanentError_TransitionsToTerminal verifies that
// when the runtime reconnect loop encounters a broker-classified permanent
// error (Recover=false on AMQP protocol errors, or structural errors like
// URI/TLS), the connection transitions to StateTerminal, closes terminalCh,
// records the permanent error, and returns false (causing reconnectLoop to
// exit so the goroutine does not spin forever).
//
// This reverses the post-PR#173 "A.1" semantics where runtime reconnect never
// classified permanent errors and retried indefinitely (causing pods with
// revoked credentials to never recover via /readyz=503).
func TestReconnectWithBackoff_PermanentError_TransitionsToTerminal(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{
			name: "AMQP 403 ACCESS_REFUSED (creds revoked)",
			err:  &amqp.Error{Code: 403, Reason: "ACCESS_REFUSED", Server: true, Recover: false},
		},
		{
			name: "AMQP 404 NOT_FOUND (vhost deleted)",
			err:  &amqp.Error{Code: 404, Reason: "NOT_FOUND", Server: true, Recover: false},
		},
		{
			name: "AMQP 530 NOT_ALLOWED",
			err:  &amqp.Error{Code: 530, Reason: "NOT_ALLOWED", Server: true, Recover: false},
		},
		{
			name: "wrapped permanent (errors.Is via fmt.Errorf %w)",
			err:  fmt.Errorf("dial: %w", &amqp.Error{Code: 403, Reason: "ACCESS_REFUSED", Server: true, Recover: false}),
		},
		{
			name: "AMQP URI parse failure (plain error string fallback)",
			err:  errors.New("AMQP URI must start with amqp:// or amqps://"),
		},
		{
			name: "x509 certificate error (string fallback)",
			err:  errors.New("x509: certificate signed by unknown authority"),
		},
		{
			name: "TLS handshake error (string fallback)",
			err:  errors.New("tls: handshake failure"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			closeCh := make(chan struct{})
			defer close(closeCh)

			conn := &Connection{
				config: Config{
					URL:                 testAMQPURL,
					ReconnectBaseDelay:  testtime.D1ms,
					ReconnectMaxBackoff: testtime.FastPoll,
				},
				dial: func(string) (AMQPConnection, error) {
					return nil, tt.err
				},
				closeCh:    closeCh,
				connected:  make(chan struct{}),
				terminalCh: make(chan struct{}),
				clock:      clock.Real(),
				state:      StateDisconnected,
			}

			ok := conn.reconnectWithBackoff()
			assert.False(t, ok, "must return false on permanent error so reconnectLoop exits")

			// terminalCh must be closed.
			select {
			case <-conn.terminalCh:
			default:
				t.Fatal("terminalCh must be closed after permanent error")
			}

			// permanentErr set with the expected error code.
			conn.mu.RLock()
			permErr := conn.permanentErr
			state := conn.state
			conn.mu.RUnlock()

			require.NotNil(t, permErr, "permanentErr must be set")
			var ecErr *errcode.Error
			require.True(t, errors.As(permErr, &ecErr), "permanentErr must wrap *errcode.Error")
			assert.Equal(t, ErrAdapterAMQPConnectPermanent, ecErr.Code,
				"permanentErr code must be ErrAdapterAMQPConnectPermanent")
			assert.Equal(t, StateTerminal, state, "state must transition to StateTerminal")

			// Health() reports the permanent error.
			healthErr := conn.Health(context.Background())
			require.Error(t, healthErr, "Health() must return error in terminal state")
			require.True(t, errors.As(healthErr, &ecErr))
			assert.Equal(t, ErrAdapterAMQPConnectPermanent, ecErr.Code)

			// isTerminalConnectionError must classify it as terminal.
			assert.True(t, isTerminalConnectionError(healthErr),
				"isTerminalConnectionError must return true for runtime permanent error")
		})
	}
}

// TestReconnectWithBackoff_TransientError_StaysReconnecting verifies the
// negative case: transient dial errors (network refused, DNS, AMQP errors
// with Recover=true) must NOT trigger StateTerminal — they keep retrying
// indefinitely with capped exponential backoff. Only closeCh stops the loop.
func TestReconnectWithBackoff_TransientError_StaysReconnecting(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{
			name: "net.OpError connection refused",
			err:  &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")},
		},
		{
			name: "AMQP 320 CONNECTION_FORCED (Recover=true)",
			err:  &amqp.Error{Code: 320, Reason: "CONNECTION_FORCED", Server: true, Recover: true},
		},
		{
			name: "AMQP 501 mid-handshake reset (Recover=false but transient — see ContinuesIndefinitely test)",
			err:  &amqp.Error{Code: 501, Reason: "read: connection reset by peer", Recover: false},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			closeCh := make(chan struct{})

			var mu sync.Mutex
			dialCount := 0

			conn := &Connection{
				config: Config{
					URL:                 testAMQPURL,
					ReconnectBaseDelay:  testtime.D1ms,
					ReconnectMaxBackoff: testtime.FastPoll,
				},
				dial: func(string) (AMQPConnection, error) {
					mu.Lock()
					dialCount++
					mu.Unlock()
					return nil, tt.err
				},
				closeCh:    closeCh,
				connected:  make(chan struct{}),
				terminalCh: make(chan struct{}),
				clock:      clock.Real(),
				state:      StateDisconnected,
			}

			done := make(chan bool, 1)
			go func() {
				done <- conn.reconnectWithBackoff()
			}()

			// Wait for at least 3 retries so we can prove "indefinite" behavior.
			require.Eventually(t, func() bool {
				mu.Lock()
				defer mu.Unlock()
				return dialCount >= 3
			}, testtime.D2s, testtime.D1ms,
				"must retry at least 3 times for transient errors (no terminal transition)")

			// terminalCh must NOT be closed.
			select {
			case <-conn.terminalCh:
				t.Fatal("terminalCh must NOT be closed for transient errors")
			default:
			}

			conn.mu.RLock()
			permErr := conn.permanentErr
			state := conn.state
			conn.mu.RUnlock()

			assert.Nil(t, permErr, "permanentErr must remain nil for transient errors")
			assert.NotEqual(t, StateTerminal, state, "state must NOT be StateTerminal for transient errors")

			// Stop the loop.
			close(closeCh)
			select {
			case ok := <-done:
				assert.False(t, ok, "must return false when closeCh fires")
			case <-time.After(testtime.D2s):
				t.Fatal("reconnectWithBackoff did not return after closeCh")
			}
		})
	}
}

// TestReconnectLoop_PermanentError_ExitsLoop verifies the full reconnectLoop
// path: established connection → NotifyClose triggered → reconnectWithBackoff
// hits permanent dial error → loop exits → Health() returns permanent error
// (so /readyz returns 503 to k8s).
func TestReconnectLoop_PermanentError_ExitsLoop(t *testing.T) {
	var mu sync.Mutex
	dialCount := 0
	mock := newMockConnection()
	permanentDialErr := &amqp.Error{Code: 403, Reason: "ACCESS_REFUSED", Server: true, Recover: false}

	conn, err := NewConnection(Config{
		URL:                 testAMQPURL,
		ChannelPoolSize:     2,
		ReconnectBaseDelay:  testtime.D1ms,
		ReconnectMaxBackoff: testtime.FastPoll,
	}, WithDialFunc(func(string) (AMQPConnection, error) {
		mu.Lock()
		dialCount++
		n := dialCount
		mu.Unlock()
		if n == 1 {
			return mock, nil
		}
		return nil, permanentDialErr
	}), WithConnectionClock(clock.Real()))
	require.NoError(t, err)
	defer func() {
		if cErr := conn.Close(context.Background()); cErr != nil {
			t.Logf("conn.Close: %v", cErr)
		}
	}()

	// Wait for reconnectLoop to register NotifyClose.
	require.Eventually(t, func() bool {
		mock.mu.Lock()
		defer mock.mu.Unlock()
		return mock.notifyCloseCh != nil
	}, time.Second, time.Millisecond, "reconnectLoop did not call NotifyClose")

	// Trigger broker-side close (e.g. admin revoked credentials and broker dropped us).
	mock.mu.Lock()
	closeNotifyCh := mock.notifyCloseCh
	mock.isClosed = true
	mock.mu.Unlock()
	closeNotifyCh <- &amqp.Error{Code: 320, Reason: "CONNECTION_FORCED", Recover: true}

	// Reconnect attempt sees permanent dial error → terminal.
	require.Eventually(t, func() bool {
		select {
		case <-conn.terminalCh:
			return true
		default:
			return false
		}
	}, testtime.EventuallyLong, testtime.D1ms,
		"terminalCh should close after permanent dial error during reconnect")

	// Health() returns the permanent error.
	healthErr := conn.Health(context.Background())
	require.Error(t, healthErr)
	var ecErr *errcode.Error
	require.True(t, errors.As(healthErr, &ecErr))
	assert.Equal(t, ErrAdapterAMQPConnectPermanent, ecErr.Code,
		"Health() must surface ErrAdapterAMQPConnectPermanent so /readyz returns 503")

	// WaitConnected returns the permanent error (so subscriber/publisher exit cleanly).
	ctx, cancel := context.WithTimeout(context.Background(), testtime.D2s)
	defer cancel()
	waitErr := conn.WaitConnected(ctx)
	require.Error(t, waitErr, "WaitConnected must return permanent error in terminal state")
	require.True(t, errors.As(waitErr, &ecErr))
	assert.Equal(t, ErrAdapterAMQPConnectPermanent, ecErr.Code)
	assert.True(t, isTerminalConnectionError(waitErr),
		"isTerminalConnectionError must classify the WaitConnected return as terminal "+
			"(subscriber.go:376/508 + publisher.go:107 rely on this for clean exit)")
}

// TestReconnectWithBackoff_PermanentError_DoesNotLeakCredentials verifies that
// the permanent error stored on the Connection has its dial error sanitized so
// callers reading Health()/WaitConnected do not see broker URL credentials.
func TestReconnectWithBackoff_PermanentError_DoesNotLeakCredentials(t *testing.T) {
	closeCh := make(chan struct{})
	defer close(closeCh)

	conn := &Connection{
		config: Config{
			URL:                 testAMQPAdminURL, // contains admin:secret123
			ReconnectBaseDelay:  testtime.D1ms,
			ReconnectMaxBackoff: testtime.FastPoll,
		},
		dial: func(string) (AMQPConnection, error) {
			// Wrap the URL into the error so we can prove redaction works.
			return nil, fmt.Errorf("dial %s: %w", testAMQPAdminURL,
				&amqp.Error{Code: 403, Reason: "ACCESS_REFUSED", Server: true, Recover: false})
		},
		closeCh:    closeCh,
		connected:  make(chan struct{}),
		terminalCh: make(chan struct{}),
		clock:      clock.Real(),
		state:      StateDisconnected,
	}

	ok := conn.reconnectWithBackoff()
	require.False(t, ok)

	conn.mu.RLock()
	permErr := conn.permanentErr
	lastError := conn.lastError
	conn.mu.RUnlock()

	require.NotNil(t, permErr)
	assert.NotContains(t, permErr.Error(), "secret123",
		"permanentErr must not leak credentials")
	assert.NotContains(t, lastError, "secret123",
		"lastError must not leak credentials")
}
