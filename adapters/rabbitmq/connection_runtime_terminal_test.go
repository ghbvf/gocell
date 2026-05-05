package rabbitmq

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// =============================================================================
// classifyDialError — unit-level table for the runtime classifier.
// Single source of truth for which dial errors map to which class. Higher-level
// reconnect tests below verify the classifier is wired correctly into the
// retry loop.
// =============================================================================

func TestClassifyDialError(t *testing.T) {
	// Real ParseURI products (rather than hand-crafted strings) so the
	// classifier is validated against the actual amqp091-go error surface.
	_, malformedSchemeErr := amqp.ParseURI("ftp://localhost")
	require.Error(t, malformedSchemeErr, "amqp.ParseURI must reject ftp:// scheme")
	_, missingPortErr := amqp.ParseURI("amqp://host:notaport")
	require.Error(t, missingPortErr, "amqp.ParseURI must reject non-numeric port")

	tests := []struct {
		name string
		err  error
		want permanentDialClass
	}{
		// nil / unknown
		{"nil", nil, permanentClassNone},
		{"unknown plain error", errors.New("anything else"), permanentClassNone},

		// Inferred sentinels — amqp091-go infers these from socket close.
		// A single hit must NOT promote (transient handshake fault could
		// produce the same observation); the reconnect loop confirms via
		// runtimePermanentConfirmHits.
		{"ErrCredentials sentinel (P0 path)", amqp.ErrCredentials, permanentClassInferred},
		{"ErrVhost sentinel", amqp.ErrVhost, permanentClassInferred},
		{"wrapped ErrCredentials", fmt.Errorf("dial tune: %w", amqp.ErrCredentials), permanentClassInferred},

		// Definitive sentinels — amqp091-go protocol/library hard errors.
		{"ErrSASL sentinel", amqp.ErrSASL, permanentClassDefinitive},
		{"ErrSyntax sentinel", amqp.ErrSyntax, permanentClassDefinitive},
		{"ErrFrame sentinel", amqp.ErrFrame, permanentClassDefinitive},
		{"ErrCommandInvalid sentinel", amqp.ErrCommandInvalid, permanentClassDefinitive},
		{"ErrUnexpectedFrame sentinel", amqp.ErrUnexpectedFrame, permanentClassDefinitive},

		// Broker-emitted *amqp.Error frames (Server=true && !Recover).
		{
			"AMQP 403 ACCESS_REFUSED Server=true Recover=false",
			&amqp.Error{Code: 403, Reason: "ACCESS_REFUSED", Server: true, Recover: false},
			permanentClassDefinitive,
		},
		{
			"AMQP 404 NOT_FOUND Server=true Recover=false",
			&amqp.Error{Code: 404, Reason: "NOT_FOUND", Server: true, Recover: false},
			permanentClassDefinitive,
		},
		{
			"AMQP 530 NOT_ALLOWED Server=true Recover=false",
			&amqp.Error{Code: 530, Reason: "NOT_ALLOWED", Server: true, Recover: false},
			permanentClassDefinitive,
		},
		{
			"wrapped broker AMQP 403",
			fmt.Errorf("dial: %w", &amqp.Error{Code: 403, Reason: "ACCESS_REFUSED", Server: true, Recover: false}),
			permanentClassDefinitive,
		},

		// Server=false *amqp.Error — amqp091-go local synthesis (transport fault).
		// Must remain transient (broker-restart races produce 501 Server=false).
		{
			"AMQP 501 Server=false Recover=false (mid-handshake reset)",
			&amqp.Error{Code: 501, Reason: "read: connection reset by peer", Server: false, Recover: false},
			permanentClassNone,
		},
		{
			"AMQP 501 Server=true Recover=true (recoverable broker)",
			&amqp.Error{Code: 501, Reason: "FRAME_ERROR", Server: true, Recover: true},
			permanentClassNone,
		},
		{
			"AMQP 320 CONNECTION_FORCED Server=true Recover=true",
			&amqp.Error{Code: 320, Reason: "CONNECTION_FORCED", Server: true, Recover: true},
			permanentClassNone,
		},

		// URI parse failures (real amqp.ParseURI products).
		{"amqp.ParseURI rejects ftp:// scheme", malformedSchemeErr, permanentClassDefinitive},
		{"amqp.ParseURI rejects non-numeric port", missingPortErr, permanentClassDefinitive},

		// Network-level errors → recoverable.
		{
			"net.OpError connection refused",
			&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")},
			permanentClassNone,
		},

		// String-fallback (TLS/x509 plain errors with no typed shape).
		{"x509 certificate error", errors.New("x509: certificate signed by unknown authority"), permanentClassDefinitive},
		{"TLS handshake error", errors.New("tls: handshake failure"), permanentClassDefinitive},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyDialError(tt.err)
			assert.Equal(t, tt.want, got, "classifyDialError(%v) = %d, want %d", tt.err, got, tt.want)
		})
	}
}

// =============================================================================
// reconnectWithBackoff — definitive sentinel single-hit promotion.
// =============================================================================

// TestReconnectWithBackoff_DefinitivePermanent_PromotesOnFirstHit drives the
// reconnect loop with a definitive permanent dial error (broker-emitted 403
// frame with Server=true). After a single hit the connection must:
//   - record permanentErr (Health and WaitConnected return it)
//   - keep the reconnect goroutine alive (no return from loop) so an
//     operator fix self-heals on the next successful dial
//   - leave state at StateDisconnected (no terminal phase)
func TestReconnectWithBackoff_DefinitivePermanent_PromotesOnFirstHit(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{
			"AMQP 403 ACCESS_REFUSED (broker-emitted)",
			&amqp.Error{Code: 403, Reason: "ACCESS_REFUSED", Server: true, Recover: false},
		},
		{
			"AMQP 404 NOT_FOUND (broker-emitted)",
			&amqp.Error{Code: 404, Reason: "NOT_FOUND", Server: true, Recover: false},
		},
		{
			"AMQP 530 NOT_ALLOWED (broker-emitted)",
			&amqp.Error{Code: 530, Reason: "NOT_ALLOWED", Server: true, Recover: false},
		},
		{
			"wrapped broker AMQP 403",
			fmt.Errorf("dial: %w", &amqp.Error{Code: 403, Reason: "ACCESS_REFUSED", Server: true, Recover: false}),
		},
		{"amqp.ErrSASL sentinel", amqp.ErrSASL},
		{"amqp.ErrSyntax sentinel", amqp.ErrSyntax},
		{"x509 string fallback", errors.New("x509: certificate signed by unknown authority")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			closeCh := make(chan struct{})
			defer close(closeCh)

			var dialCount atomic.Int32
			conn := &Connection{
				config: Config{
					URL:                 testAMQPURL,
					ReconnectBaseDelay:  testtime.D1ms,
					ReconnectMaxBackoff: testtime.FastPoll,
				},
				dial: func(string) (AMQPConnection, error) {
					dialCount.Add(1)
					return nil, tt.err
				},
				closeCh:   closeCh,
				connected: make(chan struct{}),
				clock:     clock.Real(),
				state:     StateDisconnected,
			}

			done := make(chan bool, 1)
			go func() { done <- conn.reconnectWithBackoff() }()

			// Wait until reconnect loop has classified once — permanentErr set.
			require.Eventually(t, func() bool {
				conn.mu.RLock()
				defer conn.mu.RUnlock()
				return conn.permanentErr != nil
			}, testtime.D2s, testtime.D1ms,
				"definitive permanent error must promote to permanentErr on first hit")

			// State remains StateDisconnected — no terminal phase exists.
			conn.mu.RLock()
			state := conn.state
			conn.mu.RUnlock()
			assert.Equal(t, StateDisconnected, state,
				"state must remain StateDisconnected — permanentErr supersedes phase, no hard terminal")

			// Health surfaces the permanent error (so /readyz returns 503).
			healthErr := conn.Health(context.Background())
			require.Error(t, healthErr)
			var ecErr *errcode.Error
			require.True(t, errors.As(healthErr, &ecErr))
			assert.Equal(t, ErrAdapterAMQPConnectPermanent, ecErr.Code)

			// isTerminalConnectionError still classifies it for subscribers/publishers.
			assert.True(t, isTerminalConnectionError(healthErr))

			// Reconnect goroutine MUST stay alive (no return from inner loop).
			// We prove this by checking dialCount keeps growing after promotion.
			countAtPromotion := dialCount.Load()
			require.Eventually(t, func() bool {
				return dialCount.Load() > countAtPromotion
			}, testtime.D2s, testtime.D1ms,
				"reconnect goroutine must keep dialing after permanent classification "+
					"(self-heal path: operator fix → next dial succeeds → markRecovered)")

			select {
			case <-done:
				t.Fatal("reconnectWithBackoff returned — must stay in retry loop after permanent classification")
			default:
			}
		})
	}
}

// =============================================================================
// reconnectWithBackoff — inferred sentinel requires confirmation.
// =============================================================================

// TestReconnectWithBackoff_InferredSentinel_RequiresConfirmation verifies that
// a single ErrCredentials/ErrVhost hit does NOT immediately classify as
// permanent. amqp091-go infers these from socket close (connection.go:1043 /
// :1096), so a transient handshake fault produces the same observation. The
// reconnect loop must accumulate runtimePermanentConfirmHits consecutive
// hits before promoting.
func TestReconnectWithBackoff_InferredSentinel_RequiresConfirmation(t *testing.T) {
	tests := []struct {
		name     string
		sentinel error
	}{
		{"ErrCredentials (P0 revoked credentials path)", amqp.ErrCredentials},
		{"ErrVhost (vhost deleted / no access)", amqp.ErrVhost},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			closeCh := make(chan struct{})

			var dialCount atomic.Int32
			conn := &Connection{
				config: Config{
					URL:                 testAMQPURL,
					ReconnectBaseDelay:  testtime.D1ms,
					ReconnectMaxBackoff: testtime.FastPoll,
				},
				dial: func(string) (AMQPConnection, error) {
					dialCount.Add(1)
					return nil, tt.sentinel
				},
				closeCh:   closeCh,
				connected: make(chan struct{}),
				clock:     clock.Real(),
				state:     StateDisconnected,
			}

			done := make(chan bool, 1)
			go func() { done <- conn.reconnectWithBackoff() }()

			// After confirmThreshold hits, permanentErr must be set.
			require.Eventually(t, func() bool {
				conn.mu.RLock()
				defer conn.mu.RUnlock()
				return conn.permanentErr != nil && dialCount.Load() >= int32(runtimePermanentConfirmHits)
			}, testtime.D2s, testtime.D1ms,
				"inferred sentinel must promote after %d consecutive hits",
				runtimePermanentConfirmHits)

			close(closeCh)
			<-done
		})
	}
}

// TestReconnectWithBackoff_InferredSentinel_TransientThenSuccess verifies that
// a single ErrCredentials hit followed by a successful dial does NOT promote
// to permanent. This is the false-positive guard: a network blip mid-handshake
// shows up as ErrCredentials but the very next attempt succeeds; /readyz must
// stay 200 throughout.
func TestReconnectWithBackoff_InferredSentinel_TransientThenSuccess(t *testing.T) {
	closeCh := make(chan struct{})
	defer close(closeCh)

	var dialCount atomic.Int32
	mock := newMockConnection()

	conn := &Connection{
		config: Config{
			URL:                 testAMQPURL,
			ReconnectBaseDelay:  testtime.D1ms,
			ReconnectMaxBackoff: testtime.FastPoll,
		},
		dial: func(string) (AMQPConnection, error) {
			n := dialCount.Add(1)
			if n == 1 {
				return nil, amqp.ErrCredentials
			}
			return mock, nil
		},
		closeCh:   closeCh,
		connected: make(chan struct{}),
		clock:     clock.Real(),
		state:     StateDisconnected,
	}

	ok := conn.reconnectWithBackoff()
	assert.True(t, ok, "reconnectWithBackoff must return true on success after one transient inferred-sentinel hit")

	conn.mu.RLock()
	permErr := conn.permanentErr
	pendingHits := conn.pendingPermanentHits
	conn.mu.RUnlock()

	assert.Nil(t, permErr, "permanentErr must NOT be set after a single inferred-sentinel hit followed by success")
	assert.Equal(t, 0, pendingHits, "pending hits counter must be cleared by markRecovered")
	// Note: Health() still returns errHealthReconnecting here because we
	// invoked reconnectWithBackoff directly without the surrounding
	// reconnectLoop that sets state=StateConnected after success.
	// TestReconnectLoop_PermanentAndRecovery covers the Health-recovers path
	// end-to-end via NewConnection.
}

// =============================================================================
// reconnectWithBackoff — transient errors keep retrying, no promotion.
// =============================================================================

func TestReconnectWithBackoff_TransientError_StaysReconnecting(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{
			"net.OpError connection refused",
			&net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")},
		},
		{
			"AMQP 320 CONNECTION_FORCED (Recover=true)",
			&amqp.Error{Code: 320, Reason: "CONNECTION_FORCED", Server: true, Recover: true},
		},
		{
			"AMQP 501 Server=false Recover=false (mid-handshake reset)",
			&amqp.Error{Code: 501, Reason: "read: connection reset by peer", Server: false, Recover: false},
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
				closeCh:   closeCh,
				connected: make(chan struct{}),
				clock:     clock.Real(),
				state:     StateDisconnected,
			}

			done := make(chan bool, 1)
			go func() { done <- conn.reconnectWithBackoff() }()

			require.Eventually(t, func() bool {
				mu.Lock()
				defer mu.Unlock()
				return dialCount >= 3
			}, testtime.D2s, testtime.D1ms,
				"transient errors must keep retrying without promotion")

			conn.mu.RLock()
			permErr := conn.permanentErr
			pendingHits := conn.pendingPermanentHits
			conn.mu.RUnlock()
			assert.Nil(t, permErr, "permanentErr must remain nil for transient errors")
			assert.Equal(t, 0, pendingHits, "pending hits must remain 0 for non-inferred errors")

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

// =============================================================================
// End-to-end reconnect loop: NotifyClose → permanent → self-heal on recovery.
// =============================================================================

// TestReconnectLoop_PermanentAndRecovery exercises the full self-heal path:
//  1. Connection established
//  2. broker close notification fires → reconnect kicks in
//  3. dial returns ErrCredentials × confirmThreshold → permanentErr set,
//     /readyz returns 503, but reconnect goroutine stays alive
//  4. dial starts succeeding (operator restored credentials) → markRecovered
//     clears permanentErr, /readyz returns 200, WaitConnected unblocks
//
// This is the regression test that prevents reverting to the hard-terminal
// design. If reconnectWithBackoff exits the inner loop on permanent
// classification, step 4 never runs and the test deadlocks.
func TestReconnectLoop_PermanentAndRecovery(t *testing.T) {
	originalMock := newMockConnection()
	recoveredMock := newMockConnection()

	// dialPhase orchestrates three regimes:
	//  - phase=0: initial dial succeeds (returns originalMock)
	//  - phase=1: ErrCredentials × confirmThreshold (revoked credentials)
	//  - phase=2: success again (operator restored credentials)
	var phase atomic.Int32
	var failureHits atomic.Int32

	dialFunc := func(string) (AMQPConnection, error) {
		switch phase.Load() {
		case 0:
			return originalMock, nil
		case 1:
			hits := failureHits.Add(1)
			if hits >= int32(runtimePermanentConfirmHits)+2 {
				// Once the loop has had time to confirm + retry once more,
				// shift to recovery.
				phase.Store(2)
				return recoveredMock, nil
			}
			return nil, amqp.ErrCredentials
		default:
			return recoveredMock, nil
		}
	}

	conn, err := NewConnection(Config{
		URL:                 testAMQPURL,
		ChannelPoolSize:     2,
		ReconnectBaseDelay:  testtime.D1ms,
		ReconnectMaxBackoff: testtime.FastPoll,
	}, WithDialFunc(dialFunc), WithConnectionClock(clock.Real()))
	require.NoError(t, err)
	defer func() {
		if cErr := conn.Close(context.Background()); cErr != nil {
			t.Logf("conn.Close: %v", cErr)
		}
	}()

	// Phase 0 → 1: trigger broker-side close on the original connection.
	require.Eventually(t, func() bool {
		originalMock.mu.Lock()
		defer originalMock.mu.Unlock()
		return originalMock.notifyCloseCh != nil
	}, time.Second, time.Millisecond)

	phase.Store(1)
	originalMock.mu.Lock()
	closeNotifyCh := originalMock.notifyCloseCh
	originalMock.isClosed = true
	originalMock.mu.Unlock()
	closeNotifyCh <- &amqp.Error{Code: 320, Reason: "CONNECTION_FORCED", Recover: true}

	// Permanent classification must surface — Health returns ErrAdapterAMQPConnectPermanent.
	require.Eventually(t, func() bool {
		err := conn.Health(context.Background())
		var ecErr *errcode.Error
		return err != nil && errors.As(err, &ecErr) && ecErr.Code == ErrAdapterAMQPConnectPermanent
	}, testtime.EventuallyLong, testtime.D1ms,
		"after %d ErrCredentials hits, Health must return ErrAdapterAMQPConnectPermanent",
		runtimePermanentConfirmHits)

	// Phase 2: dial succeeds → reconnect loop must promote back to healthy.
	require.Eventually(t, func() bool {
		return conn.Health(context.Background()) == nil
	}, testtime.EventuallyLong, testtime.D1ms,
		"once dial starts succeeding, the reconnect loop must clear permanentErr and Health must return nil")

	// WaitConnected must observe the recovery.
	ctx, cancel := context.WithTimeout(context.Background(), testtime.EventuallyLong)
	defer cancel()
	assert.NoError(t, conn.WaitConnected(ctx),
		"WaitConnected must return nil after self-heal")
}

// =============================================================================
// Credential / URL redaction.
// =============================================================================

// TestSanitizeURL_RedactsCredentialsAndQuery covers all sanitizeURL paths
// including the RawQuery drop (RabbitMQ URI query parameters can carry
// password/cacertfile/keyfile per https://www.rabbitmq.com/docs/uri-query-parameters).
// All URLs in the table are fake fixtures used to exercise the redactor.
//
//nolint:gosec // G101: fake creds in test fixtures, not real secrets.
func TestSanitizeURL_RedactsCredentialsAndQuery(t *testing.T) {
	tests := []struct {
		name   string
		raw    string
		wanted []string // substrings the result MUST contain
		denied []string // substrings the result MUST NOT contain
	}{
		{
			name:   "empty URL",
			raw:    "",
			wanted: []string{"amqp://***"},
		},
		{
			name:   "userinfo redacted",
			raw:    "amqp://user:secret123@broker.example.com:5672/vhost",
			wanted: []string{"amqp://***:***@broker.example.com:5672/vhost"},
			denied: []string{"user", "secret123"},
		},
		{
			name:   "userinfo + query (?password) redacted",
			raw:    "amqp://user:secret@broker:5672/vh?password=qsecret&heartbeat=60", //nolint:gosec // G101: fake creds for redactor test
			wanted: []string{"amqp://***:***@broker:5672/vh"},
			denied: []string{"user", "secret", "qsecret", "heartbeat=60", "password"},
		},
		{
			name:   "no userinfo, ?password query still dropped",
			raw:    "amqp://broker:5672/vh?password=qsecret",
			wanted: []string{"amqp://broker:5672/vh"},
			denied: []string{"qsecret", "password"},
		},
		{
			name:   "no userinfo, TLS query dropped",
			raw:    "amqps://broker:5671/vh?cacertfile=/etc/ca.pem&keyfile=/etc/key.pem",
			wanted: []string{"amqps://broker:5671/vh"},
			denied: []string{"cacertfile", "keyfile", "/etc/ca.pem", "/etc/key.pem"},
		},
		{
			name:   "fragment dropped",
			raw:    "amqp://broker:5672/vh#some-fragment",
			wanted: []string{"amqp://broker:5672/vh"},
			denied: []string{"some-fragment"},
		},
		{
			name:   "userinfo with ? in password (URL-encoded)",
			raw:    "amqp://user:p%3Fass@broker:5672/vh",
			wanted: []string{"amqp://***:***@broker:5672/vh"},
			denied: []string{"p%3Fass", "p?ass"},
		},
		{
			name:   "no credentials, no query — passthrough",
			raw:    "amqp://broker.local:5672/vhost",
			wanted: []string{"amqp://broker.local:5672/vhost"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeURL(tt.raw)
			for _, w := range tt.wanted {
				assert.Contains(t, got, w)
			}
			for _, d := range tt.denied {
				assert.NotContains(t, got, d, "sanitizeURL must not leak %q", d)
			}
		})
	}
}

// TestReconnectWithBackoff_PermanentError_DoesNotLeakCredentials reuses the
// existing credential-leak guard but for the runtime path (rather than just
// startup-time). It verifies both userinfo *and* query parameters are redacted
// in the recorded permanentErr / lastError.
func TestReconnectWithBackoff_PermanentError_DoesNotLeakCredentials(t *testing.T) {
	closeCh := make(chan struct{})
	defer close(closeCh)

	// fake creds for redactor test, not real secrets.
	const adminURL = "amqp://admin:secret123@broker.example.com:5672/vhost?password=qsecret" //nolint:gosec // G101

	conn := &Connection{
		config: Config{
			URL:                 adminURL,
			ReconnectBaseDelay:  testtime.D1ms,
			ReconnectMaxBackoff: testtime.FastPoll,
		},
		dial: func(string) (AMQPConnection, error) {
			return nil, fmt.Errorf("dial %s: %w", adminURL,
				&amqp.Error{Code: 403, Reason: "ACCESS_REFUSED", Server: true, Recover: false})
		},
		closeCh:   closeCh,
		connected: make(chan struct{}),
		clock:     clock.Real(),
		state:     StateDisconnected,
	}

	go func() { _ = conn.reconnectWithBackoff() }()

	require.Eventually(t, func() bool {
		conn.mu.RLock()
		defer conn.mu.RUnlock()
		return conn.permanentErr != nil
	}, testtime.D2s, testtime.D1ms)

	conn.mu.RLock()
	permErrText := conn.permanentErr.Error()
	lastError := conn.lastError
	conn.mu.RUnlock()

	for _, secret := range []string{"secret123", "qsecret", "admin:"} {
		assert.NotContains(t, permErrText, secret,
			"permanentErr must not leak %q", secret)
		assert.NotContains(t, lastError, secret,
			"lastError must not leak %q", secret)
	}
}
