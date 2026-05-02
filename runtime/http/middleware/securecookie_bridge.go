package middleware

import (
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/securecookie"
)

// securecookieBridgeCheck is a compile-time assertion that kernel/clock.Clock
// satisfies pkg/securecookie.Clock structurally. pkg/ may not import kernel/,
// so this interface-compatibility check lives here in runtime/http/middleware
// where both packages are already in scope.
//
// If the two Clock interfaces ever diverge (e.g. a Now() signature change),
// this file will fail to compile, surfacing the inconsistency immediately.
var _ securecookie.Clock = (clock.Clock)(nil)
