package websocket

import "github.com/ghbvf/gocell/pkg/errcode"

// Adapter-level error codes for the nhooyr.io/websocket binding.
const (
	// ErrAdapterWSUpgrade indicates a WebSocket upgrade failure.
	ErrAdapterWSUpgrade errcode.Code = "ERR_ADAPTER_WS_UPGRADE"
	// ErrAdapterWSWrite indicates a WebSocket write failure.
	ErrAdapterWSWrite errcode.Code = "ERR_ADAPTER_WS_WRITE"
	// ErrAdapterWSClosed indicates an operation on a closed connection.
	ErrAdapterWSClosed errcode.Code = "ERR_ADAPTER_WS_CLOSED"
)
