package websocket

import "github.com/ghbvf/gocell/pkg/errcode"

// Hub error codes.
const (
	// ErrWSConnNotFound indicates Send targeted a non-existent connection.
	ErrWSConnNotFound errcode.Code = "ERR_WS_CONN_NOT_FOUND"

	// ErrWSAlreadyStarted indicates Start was called on a running Hub.
	ErrWSAlreadyStarted errcode.Code = "ERR_WS_ALREADY_STARTED"

	// ErrWSAlreadyStopped indicates Start or Stop was called on a stopped Hub.
	ErrWSAlreadyStopped errcode.Code = "ERR_WS_ALREADY_STOPPED"

	// ErrWSHubStopping indicates Register was called during shutdown.
	ErrWSHubStopping errcode.Code = "ERR_WS_HUB_STOPPING"

	// ErrWSHubNotRunning indicates Register was called on a non-running Hub
	// (idle or stopped).
	ErrWSHubNotRunning errcode.Code = "ERR_WS_HUB_NOT_RUNNING"

	// ErrWSMaxConns indicates Register was rejected because the Hub has
	// reached its MaxConnections limit.
	ErrWSMaxConns errcode.Code = "ERR_WS_MAX_CONNS"
)
