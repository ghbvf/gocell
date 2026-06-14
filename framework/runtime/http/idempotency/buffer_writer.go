package idempotency

import (
	"io"
	"net/http"

	"github.com/felixge/httpsnoop"
)

// bufferingWriter is a tee ResponseWriter that forwards all writes to the
// underlying writer while also buffering the response body up to maxBody bytes.
// Once the buffer would exceed maxBody the oversized flag is set and buffering
// stops (but forwarding to the real writer continues uninterrupted so the
// client always receives the full response).
//
// Constructed via newBufferingWriter; never used as a direct value.
type bufferingWriter struct {
	http.ResponseWriter

	s       bufferingState
	maxBody int
}

type bufferingState struct {
	statusCode int
	buf        []byte
	headerSnap http.Header
	oversized  bool
	didCommit  bool
}

// newBufferingWriter wraps w with httpsnoop so that optional interfaces
// (http.Flusher, http.Pusher, http.Hijacker, io.ReaderFrom) are preserved
// from the underlying writer.
//
// maxBody limits how many bytes are accumulated in the internal buffer.
// Bodies larger than maxBody are forwarded to the client but not buffered
// (isOversized returns true in that case).
func newBufferingWriter(w http.ResponseWriter, maxBody int) *bufferingWriter {
	bw := &bufferingWriter{maxBody: maxBody}
	bw.s.statusCode = http.StatusOK
	bw.ResponseWriter = httpsnoop.Wrap(w, httpsnoop.Hooks{
		WriteHeader: bw.writeHeaderHook(w),
		Write:       bw.writeHook(w),
		ReadFrom:    bw.readFromHook(w),
	})
	return bw
}

func (bw *bufferingWriter) writeHeaderHook(w http.ResponseWriter) func(httpsnoop.WriteHeaderFunc) httpsnoop.WriteHeaderFunc {
	return func(next httpsnoop.WriteHeaderFunc) httpsnoop.WriteHeaderFunc {
		return func(code int) {
			if code < 200 {
				// 1xx informational: forward but do not commit.
				next(code)
				return
			}
			if !bw.s.didCommit {
				bw.s.statusCode = code
				bw.s.didCommit = true
				bw.s.headerSnap = w.Header().Clone()
			}
			// Suppress duplicate WriteHeader calls (guard against superfluous call warnings).
			if code == bw.s.statusCode {
				next(code)
			}
		}
	}
}

func (bw *bufferingWriter) writeHook(w http.ResponseWriter) func(httpsnoop.WriteFunc) httpsnoop.WriteFunc {
	return func(next httpsnoop.WriteFunc) httpsnoop.WriteFunc {
		return func(b []byte) (int, error) {
			if !bw.s.didCommit {
				bw.s.didCommit = true
				bw.s.headerSnap = w.Header().Clone()
			}
			n, err := next(b)
			bw.appendToBuffer(b[:n])
			return n, err
		}
	}
}

func (bw *bufferingWriter) readFromHook(w http.ResponseWriter) func(httpsnoop.ReadFromFunc) httpsnoop.ReadFromFunc {
	return func(next httpsnoop.ReadFromFunc) httpsnoop.ReadFromFunc {
		return func(src io.Reader) (int64, error) {
			if !bw.s.didCommit {
				bw.s.didCommit = true
				bw.s.headerSnap = w.Header().Clone()
			}
			// Tee the source into our buffer up to the cap, mirroring the Write
			// hook's behavior. Only once the buffer is full do we mark oversized
			// and stop accumulating (but forwarding to the real writer continues).
			//
			// Without this, io.Copy paths (used by http.ServeContent and many
			// streaming handlers) would always mark responses as oversized even
			// when the body is tiny, breaking recording for small responses.
			available := bw.maxBody - len(bw.s.buf)
			var bufferedSrc io.Reader
			if bw.s.oversized || available <= 0 {
				// Already over cap; forward without buffering.
				bw.s.oversized = true
				bufferedSrc = src
			} else {
				// Tee into bw up to `available` bytes; after that we read the rest
				// from src directly (forwarding to the real writer via next).
				capReader := &cappedTeeReader{r: src, bw: bw, cap: available}
				bufferedSrc = capReader
			}
			return next(bufferedSrc)
		}
	}
}

// cappedTeeReader wraps an io.Reader so that the first `cap` bytes read are
// also appended into bw's buffer. Once cap is exhausted, reads continue from
// the underlying reader without buffering (oversized flag is set).
type cappedTeeReader struct {
	r   io.Reader
	bw  *bufferingWriter
	cap int
}

func (c *cappedTeeReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		c.bw.appendToBuffer(p[:n])
		// appendToBuffer adjusts c.cap indirectly via bw.s.buf length.
		// We don't need to track c.cap separately; appendToBuffer handles overflow.
		_ = c.cap // suppress unused-write lint; cap used as initial size hint only
	}
	return n, err
}

// appendToBuffer appends p to the internal buffer until maxBody is reached.
// After overflow the buffer is kept at its current size and oversized is set.
func (bw *bufferingWriter) appendToBuffer(p []byte) {
	if bw.s.oversized {
		return
	}
	available := bw.maxBody - len(bw.s.buf)
	if available <= 0 {
		bw.s.oversized = true
		return
	}
	if len(p) <= available {
		bw.s.buf = append(bw.s.buf, p...)
	} else {
		bw.s.buf = append(bw.s.buf, p[:available]...)
		bw.s.oversized = true
	}
}

// status returns the captured HTTP status code.
func (bw *bufferingWriter) status() int { return bw.s.statusCode }

// bufferedBody returns the accumulated body bytes (up to maxBody).
// The caller receives a direct slice — do not mutate.
func (bw *bufferingWriter) bufferedBody() []byte { return bw.s.buf }

// capturedHeader returns a clone of the response headers snapshotted at
// WriteHeader / first Write time.
func (bw *bufferingWriter) capturedHeader() http.Header {
	if bw.s.headerSnap == nil {
		return http.Header{}
	}
	return bw.s.headerSnap.Clone()
}

// isOversized reports whether the body exceeded maxBody.
func (bw *bufferingWriter) isOversized() bool { return bw.s.oversized }

// committed reports whether a status >= 200 or any Write has been performed.
func (bw *bufferingWriter) committed() bool { return bw.s.didCommit }
