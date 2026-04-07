// Package websocket provides an nhooyr.io/websocket binding for the
// runtime/websocket.Conn interface. It also provides an HTTP upgrade
// handler that registers new connections with a runtime/websocket.Hub.
//
// This package contains no scheduling or broadcasting logic — that
// lives in runtime/websocket.
package websocket
