package rabbitmq

import (
	"errors"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// testAMQPBlackholeURL is the RFC 5737 TEST-NET-1 address used for
// ConnectTimeout fast-fail tests. TEST-NET-1 (192.0.2.0/24) is
// documentation-only and never routed on the public internet.
//
//nolint:gosec // G101: fake fixture URL, not real credentials
var testAMQPBlackholeURL = "amqp://guest:guest@192.0.2.1:5672/"

// connectTimeoutBlackholeBudget is the upper bound for the blackhole dial
// to fail. The configured ConnectTimeout (testtime.D200ms) plus error
// classification + errcode wrapping must complete within this budget;
// exceeding it indicates the timeout did not fire and the dial fell back
// to the OS default (~1min on Linux, ~75s on macOS).
const connectTimeoutBlackholeBudget = testtime.D1s + testtime.D500ms

// connectTimeoutDefaultExpected is the documented default that
// Config.setDefaults must populate when caller leaves ConnectTimeout zero
// or negative. Mirrors defaultRMQConnectTimeout in connection.go.
const connectTimeoutDefaultExpected = testtime.D5s

// TestNewConnection_ConnectTimeout_Blackhole verifies Config.ConnectTimeout
// fast-fails dial against an unreachable RFC 5737 TEST-NET-1 IP.
//
// Without ConnectTimeout, OS default TCP SYN timeout (~1min on Linux,
// ~75s on macOS) would block NewConnection well past any sane test budget.
//
// Mirrors adapters/postgres pool TestNewPool_ConnectTimeout_Blackhole.
func TestNewConnection_ConnectTimeout_Blackhole(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("blackhole timeout test requires network reach to RFC 5737 TEST-NET-1; skipped in -short mode")
	}

	start := time.Now()
	_, err := NewConnection(Config{
		URL:            testAMQPBlackholeURL,
		ConnectTimeout: testtime.D200ms,
	}, WithConnectionClock(clock.Real()))
	elapsed := time.Since(start)

	require.Error(t, err, "NewConnection must fail; blackhole IP cannot be reached")
	require.Less(t, elapsed, connectTimeoutBlackholeBudget,
		"ConnectTimeout=200ms must fail-fast under %v; elapsed=%v indicates timeout did not fire",
		connectTimeoutBlackholeBudget, elapsed)

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr), "error must wrap *errcode.Error, got %T: %v", err, err)
	assert.Equal(t, ErrAdapterAMQPConnectTimeout, ecErr.Code,
		"blackhole dial must carry distinct timeout code, not the generic connect code")
	assert.True(t, errcode.IsTransient(err),
		"blackhole dial timeout must classify as transient (consumer Requeue path)")

	// The underlying cause must surface a net.Error with Timeout()=true,
	// proving amqp.DefaultDial(timeout) actually fired (not OS default).
	// errcode.Wrap preserves the chain via fmt.Errorf("...: %w", err); the
	// inner net.Error from amqp091's net.DialTimeout must remain reachable.
	var netErr net.Error
	require.True(t, errors.As(err, &netErr),
		"error chain must include net.Error from amqp091 TCP dial timeout; got %T: %v", err, err)
	require.True(t, netErr.Timeout(),
		"net.Error.Timeout() must be true (proves configured ConnectTimeout fired, not OS default); got %v", netErr)
}

// TestConfig_ConnectTimeout_DefaultsTo5s locks the contract that
// setDefaults populates ConnectTimeout=5s when caller leaves zero.
func TestConfig_ConnectTimeout_DefaultsTo5s(t *testing.T) {
	t.Parallel()

	cfg := Config{}
	cfg.setDefaults()
	assert.Equal(t, connectTimeoutDefaultExpected, cfg.ConnectTimeout,
		"Config.ConnectTimeout must default to 5s (defaultRMQConnectTimeout)")

	// Explicit value must be respected.
	cfg2 := Config{ConnectTimeout: testtime.D250ms}
	cfg2.setDefaults()
	assert.Equal(t, testtime.D250ms, cfg2.ConnectTimeout,
		"explicit ConnectTimeout must not be overridden")

	// Negative coerces to default (zero-value-style guard).
	cfg3 := Config{ConnectTimeout: testtime.DNeg1s}
	cfg3.setDefaults()
	assert.Equal(t, connectTimeoutDefaultExpected, cfg3.ConnectTimeout,
		"negative ConnectTimeout must coerce to default")
}
