package bootstrap

import (
	"net"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/kernel/outbox"
)

// closeListener closes a net.Listener and logs any close error.
func closeListener(t *testing.T, ln net.Listener) {
	t.Helper()
	if err := ln.Close(); err != nil {
		t.Errorf("close listener: %v", err)
	}
}

// closeBody closes resp.Body and logs any close error.
// Use in place of bare resp.Body.Close() to satisfy errcheck.
func closeBody(t *testing.T, resp *http.Response) {
	t.Helper()
	if err := resp.Body.Close(); err != nil {
		t.Errorf("close body: %v", err)
	}
}

// closeConn closes a net.Conn and logs any close error.
func closeConn(t *testing.T, conn net.Conn) {
	t.Helper()
	if err := conn.Close(); err != nil {
		t.Errorf("close conn: %v", err)
	}
}

func newTestConsumerBase(t *testing.T) *outbox.ConsumerBase {
	t.Helper()
	cb, err := outbox.NewConsumerBase(
		idempotency.NewInMemClaimer(clock.Real()),
		outbox.ConsumerBaseConfig{},
		clock.Real(),
	)
	require.NoError(t, err)
	return cb
}
