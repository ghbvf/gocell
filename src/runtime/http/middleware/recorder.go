package middleware

import (
	"context"
	"io"
	"net/http"

	"github.com/felixge/httpsnoop"
)

// RecorderState captures HTTP response metadata (status code, bytes written,
// committed state) without wrapping the ResponseWriter itself. This allows
// httpsnoop.Wrap to return a writer that preserves optional interfaces
// (http.Hijacker, http.Flusher, http.Pusher, io.ReaderFrom).
//
// Multiple middleware can share a single RecorderState via context to avoid
// redundant httpsnoop wrapping. Use RecorderStateFrom to check for an existing
// state before creating a new one.
type RecorderState struct {
	status    int
	bytes     int64
	committed bool
}

// Status returns the captured HTTP status code (default 200).
func (s *RecorderState) Status() int { return s.status }

// BytesWritten returns the total number of bytes written to the response body.
func (s *RecorderState) BytesWritten() int64 { return s.bytes }

// Committed reports whether WriteHeader (>= 200) or Write has been called.
// Informational 1xx responses do not mark the response as committed.
func (s *RecorderState) Committed() bool { return s.committed }

// NewRecorder wraps w with httpsnoop hooks that capture response metadata into
// a RecorderState. The returned http.ResponseWriter preserves all optional
// interfaces (Hijacker, Flusher, Pusher, ReaderFrom) from the original writer.
//
// 1xx informational responses (100 Continue, 103 Early Hints) are forwarded
// but do not mark the response as committed.
func NewRecorder(w http.ResponseWriter) (*RecorderState, http.ResponseWriter) {
	state := &RecorderState{status: http.StatusOK}
	wrapped := httpsnoop.Wrap(w, httpsnoop.Hooks{
		WriteHeader: func(next httpsnoop.WriteHeaderFunc) httpsnoop.WriteHeaderFunc {
			return func(code int) {
				if code >= 200 && !state.committed {
					state.status = code
					state.committed = true
				}
				if code < 200 || !state.committed || code == state.status {
					next(code)
				}
				// Suppress duplicate final WriteHeader to avoid
				// "http: superfluous response.WriteHeader call" warnings.
			}
		},
		Write: func(next httpsnoop.WriteFunc) httpsnoop.WriteFunc {
			return func(b []byte) (int, error) {
				if !state.committed {
					state.committed = true // implicit 200
				}
				n, err := next(b)
				state.bytes += int64(n)
				return n, err
			}
		},
		Flush: func(next httpsnoop.FlushFunc) httpsnoop.FlushFunc {
			return func() {
				if !state.committed {
					state.committed = true
				}
				next()
			}
		},
		ReadFrom: func(next httpsnoop.ReadFromFunc) httpsnoop.ReadFromFunc {
			return func(src io.Reader) (int64, error) {
				if !state.committed {
					state.committed = true
				}
				n, err := next(src)
				state.bytes += n
				return n, err
			}
		},
	})
	return state, wrapped
}

// ---------------------------------------------------------------------------
// Context sharing — avoid multiple httpsnoop wraps per request
// ---------------------------------------------------------------------------

type recorderCtxKey struct{}

// WithRecorderState stores a RecorderState in the context.
func WithRecorderState(ctx context.Context, s *RecorderState) context.Context {
	return context.WithValue(ctx, recorderCtxKey{}, s)
}

// RecorderStateFrom retrieves a previously stored RecorderState from the
// context. Returns nil if none exists.
func RecorderStateFrom(ctx context.Context) *RecorderState {
	s, _ := ctx.Value(recorderCtxKey{}).(*RecorderState)
	return s
}
