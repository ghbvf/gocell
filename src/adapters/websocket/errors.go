package websocket

import "github.com/ghbvf/gocell/pkg/errcode"

// WebSocket adapter error codes.
const (
	// ErrAdapterWSUpgrade indicates a WebSocket upgrade failure.
	ErrAdapterWSUpgrade errcode.Code = "ERR_ADAPTER_WS_UPGRADE"
	// ErrAdapterWSWrite indicates a WebSocket write failure.
	ErrAdapterWSWrite errcode.Code = "ERR_ADAPTER_WS_WRITE"
	// ErrAdapterWSRead indicates a WebSocket read failure.
	ErrAdapterWSRead errcode.Code = "ERR_ADAPTER_WS_READ"
	// ErrAdapterWSClosed indicates an operation on a closed connection.
	ErrAdapterWSClosed errcode.Code = "ERR_ADAPTER_WS_CLOSED"
	// ErrAdapterWSOrigin indicates an origin validation failure.
	ErrAdapterWSOrigin errcode.Code = "ERR_ADAPTER_WS_ORIGIN"
)
