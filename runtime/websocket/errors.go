package websocket

import "github.com/ghbvf/gocell/pkg/errcode"

// Hub error codes — aliases to centralized errcode constants for local readability.
var (
	ErrWSConnNotFound   = errcode.ErrWSConnNotFound
	ErrWSAlreadyStarted = errcode.ErrWSAlreadyStarted
	ErrWSAlreadyStopped = errcode.ErrWSAlreadyStopped
	ErrWSHubStopping    = errcode.ErrWSHubStopping
	ErrWSHubNotRunning  = errcode.ErrWSHubNotRunning
	ErrWSMaxConns       = errcode.ErrWSMaxConns

	ErrWebsocketAuthenticatorMissing    = errcode.ErrWebsocketAuthenticatorMissing
	ErrWebsocketBroadcastFilterMissing  = errcode.ErrWebsocketBroadcastFilterMissing
	ErrWebsocketBroadcastSubjectMissing = errcode.ErrWebsocketBroadcastSubjectMissing
	ErrWebsocketUpgradeUnauthenticated  = errcode.ErrWebsocketUpgradeUnauthenticated
	ErrWebsocketSlowClient              = errcode.ErrWebsocketSlowClient
)
