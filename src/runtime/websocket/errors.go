package websocket

import "github.com/ghbvf/gocell/pkg/errcode"

// Hub error codes.
const (
	// ErrWSConnNotFound indicates Send targeted a non-existent connection.
	ErrWSConnNotFound errcode.Code = "ERR_WS_CONN_NOT_FOUND"
	// ErrWSLifecycle indicates an invalid lifecycle transition (e.g., double Start,
	// Register during shutdown).
	ErrWSLifecycle errcode.Code = "ERR_WS_LIFECYCLE"
)
