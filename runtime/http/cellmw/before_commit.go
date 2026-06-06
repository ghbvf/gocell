package cellmw

import (
	"net/http"

	"github.com/felixge/httpsnoop"
)

// WrapBeforeCommit wraps w so that hook(status) runs exactly once, immediately
// before the response status is committed — at the explicit WriteHeader call,
// or at the implicit WriteHeader(200) on the first Write. hook is the caller's
// chance to set response headers (e.g. Set-Cookie) on w before they are flushed.
//
// The wrapper is built with httpsnoop.Wrap, which preserves the underlying
// writer's optional interfaces (Flusher / Hijacker / ReaderFrom / Pusher) —
// matching the runtime/http middleware convention (recorder.go,
// buffer_writer.go). This is the sole reason the wrapper lives here in runtime/
// rather than inline in a cells/ slice: cells/ may not import httpsnoop
// (cells-isolation depguard), so the response-writer plumbing lives in runtime/
// while the cell supplies only the header-setting hook.
func WrapBeforeCommit(w http.ResponseWriter, hook func(status int)) http.ResponseWriter {
	wrote := false
	fire := func(status int) {
		if wrote {
			return
		}
		wrote = true
		hook(status)
	}
	return httpsnoop.Wrap(w, httpsnoop.Hooks{
		WriteHeader: func(next httpsnoop.WriteHeaderFunc) httpsnoop.WriteHeaderFunc {
			return func(code int) {
				fire(code)
				next(code)
			}
		},
		Write: func(next httpsnoop.WriteFunc) httpsnoop.WriteFunc {
			return func(b []byte) (int, error) {
				fire(http.StatusOK)
				return next(b)
			}
		},
	})
}
