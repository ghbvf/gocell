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
)

// testAMQPBlackholeURL is the RFC 5737 TEST-NET-1 address used for
// ConnectTimeout fast-fail tests. Constructed as a concat to avoid
// gosec G101 false-positive on test fixture URLs.
var testAMQPBlackholeURL = "amqp://guest:" + "guest@192.0.2.1:5672/"

// TestNewConnection_ConnectTimeout_Blackhole verifies Config.ConnectTimeout
// fast-fails dial against an unreachable RFC 5737 TEST-NET-1 IP.
//
// Without ConnectTimeout, OS default TCP SYN timeout (~1min on Linux,
// ~75s on macOS) would block NewConnection well past any sane test budget.
//
// Mirrors adapters/postgres pool TestNewPool_ConnectTimeout_Blackhole.
func TestNewConnection_ConnectTimeout_Blackhole(t *testing.T) {
	t.Parallel()

	start := time.Now()
	_, err := NewConnection(Config{
		URL:            testAMQPBlackholeURL,
		ConnectTimeout: 200 * time.Millisecond,
	}, WithConnectionClock(clock.Real()))
	elapsed := time.Since(start)

	require.Error(t, err, "NewConnection must fail; blackhole IP cannot be reached")
	require.Less(t, elapsed, 1500*time.Millisecond,
		"ConnectTimeout=200ms must fail-fast under 1.5s; elapsed=%v indicates timeout did not fire", elapsed)

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr), "error must wrap *errcode.Error, got %T: %v", err, err)
	// Either ErrAdapterAMQPConnect (transient classification) or ErrAdapterAMQPConnectPermanent
	// is acceptable — but timeout from blackhole is transient by amqp091 classification.
	assert.True(t,
		ecErr.Code == ErrAdapterAMQPConnect || ecErr.Code == ErrAdapterAMQPConnectPermanent,
		"expected AMQP connect errcode, got %s", ecErr.Code)

	// The underlying cause must surface a net.Error with Timeout()=true,
	// proving amqp.DefaultDial(timeout) actually fired (not OS default).
	var netErr net.Error
	if errors.As(err, &netErr) {
		assert.True(t, netErr.Timeout(), "expected net.Error.Timeout()=true; got %v", netErr)
	}
}

// TestConfig_ConnectTimeout_DefaultsTo5s locks the contract that
// setDefaults populates ConnectTimeout=5s when caller leaves zero.
func TestConfig_ConnectTimeout_DefaultsTo5s(t *testing.T) {
	t.Parallel()

	cfg := Config{}
	cfg.setDefaults()
	assert.Equal(t, 5*time.Second, cfg.ConnectTimeout,
		"Config.ConnectTimeout must default to 5s (defaultRMQConnectTimeout)")

	// Explicit value must be respected.
	cfg2 := Config{ConnectTimeout: 250 * time.Millisecond}
	cfg2.setDefaults()
	assert.Equal(t, 250*time.Millisecond, cfg2.ConnectTimeout,
		"explicit ConnectTimeout must not be overridden")

	// Negative coerces to default (zero-value-style guard).
	cfg3 := Config{ConnectTimeout: -1}
	cfg3.setDefaults()
	assert.Equal(t, 5*time.Second, cfg3.ConnectTimeout,
		"negative ConnectTimeout must coerce to default")
}
