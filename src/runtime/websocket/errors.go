package websocket

import "github.com/ghbvf/gocell/pkg/errcode"

// Hub error codes.
const (
	// ErrWSConnNotFound indicates Send targeted a non-existent connection.
	ErrWSConnNotFound errcode.Code = "ERR_WS_CONN_NOT_FOUND"
	// ErrWSClosed indicates an operation on a closed hub or connection.
	ErrWSClosed errcode.Code = "ERR_WS_CLOSED"
)
