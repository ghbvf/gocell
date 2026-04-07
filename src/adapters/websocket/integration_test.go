//go:build integration

package websocket_test

import (
	"testing"
)

// TestIntegration_ConnectAndEcho connects a real WebSocket client, sends
// a message, and asserts the echo response.
func TestIntegration_ConnectAndEcho(t *testing.T) {
	t.Skip("stub: requires running Hub server")
}

// TestIntegration_BroadcastMultipleClients connects multiple clients and
// verifies a broadcast message reaches all of them.
func TestIntegration_BroadcastMultipleClients(t *testing.T) {
	t.Skip("stub: requires running Hub server")
}

// TestIntegration_TopicSubscribe subscribes two clients to different
// topics and asserts topic-scoped delivery.
func TestIntegration_TopicSubscribe(t *testing.T) {
	t.Skip("stub: requires running Hub server")
}

// TestIntegration_GracefulShutdown shuts down the Hub while clients are
// connected and asserts all connections are closed cleanly.
func TestIntegration_GracefulShutdown(t *testing.T) {
	t.Skip("stub: requires running Hub server")
}
