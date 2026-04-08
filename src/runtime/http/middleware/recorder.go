package middleware

import (
	"net/http"

	"github.com/felixge/httpsnoop"
)

// Recorder wraps an http.ResponseWriter to capture the status code, bytes
// written, and whether the response has been committed (WriteHeader called).
// It uses httpsnoop.Wrap to automatically preserve optional interfaces such
// as http.Hijacker, http.Flusher, and http.Pusher.
type Recorder struct {
	http.ResponseWriter
	status    int
	bytes     int64
	committed bool
}

// NewRecorder creates a Recorder that wraps w. The underlying optional
// interfaces (Hijacker, Flusher, etc.) are preserved via httpsnoop.
func NewRecorder(w http.ResponseWriter) *Recorder {
	rec := &Recorder{status: http.StatusOK}
	rec.ResponseWriter = httpsnoop.Wrap(w, httpsnoop.Hooks{
		WriteHeader: func(next httpsnoop.WriteHeaderFunc) httpsnoop.WriteHeaderFunc {
			return func(code int) {
				if !rec.committed {
					rec.status = code
					rec.committed = true
				}
				next(code)
			}
		},
		Write: func(next httpsnoop.WriteFunc) httpsnoop.WriteFunc {
			return func(b []byte) (int, error) {
				if !rec.committed {
					rec.committed = true
				}
				n, err := next(b)
				rec.bytes += int64(n)
				return n, err
			}
		},
	})
	return rec
}

// Status returns the captured HTTP status code (default 200).
func (r *Recorder) Status() int { return r.status }

// BytesWritten returns the total number of bytes written to the response body.
func (r *Recorder) BytesWritten() int64 { return r.bytes }

// Committed reports whether WriteHeader or Write has been called.
func (r *Recorder) Committed() bool { return r.committed }
