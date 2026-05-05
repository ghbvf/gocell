package websocket_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	adapterws "github.com/ghbvf/gocell/adapters/websocket"
	rtws "github.com/ghbvf/gocell/runtime/websocket"
)

// requireUpgradeHandler constructs an UpgradeHandler for test wiring.
// Fails the test immediately if construction fails (misconfigured test fixture).
func requireUpgradeHandler(t testing.TB, hub *rtws.Hub, cfg adapterws.UpgradeConfig) http.Handler {
	t.Helper()
	handler, err := adapterws.UpgradeHandler(hub, cfg)
	require.NoError(t, err)
	return handler
}
