package setup_test

import "net/http"

// testPassthroughAuth is a no-op middleware satisfying the bootstrapAuth
// requirement of setup.NewHandler. Tests that focus on schema validation or
// persistence wiring use this passthrough so they don't need to thread
// Authorization headers through every request.
//
// Tests that exercise the Basic Auth path explicitly construct a real
// runtime/auth.NewBootstrapMiddleware; see TestHandler_CreateAdmin_NoCreds_*
// and TestHandler_CreateAdmin_WrongUsername_*.
func testPassthroughAuth(next http.Handler) http.Handler { return next }
