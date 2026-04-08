package middleware

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecorder_CapturesStatus(t *testing.T) {
	state, w := NewRecorder(httptest.NewRecorder())
	w.WriteHeader(http.StatusNotFound)
	assert.Equal(t, http.StatusNotFound, state.Status())
}

func TestRecorder_DefaultStatus200(t *testing.T) {
	state, w := NewRecorder(httptest.NewRecorder())
	_, _ = w.Write([]byte("hello"))
	assert.Equal(t, http.StatusOK, state.Status())
}

func TestRecorder_BytesWritten(t *testing.T) {
	state, w := NewRecorder(httptest.NewRecorder())
	n, err := w.Write([]byte("0123456789"))
	require.NoError(t, err)
	assert.Equal(t, 10, n)
	assert.Equal(t, int64(10), state.BytesWritten())
}

func TestRecorder_Committed(t *testing.T) {
	state, w := NewRecorder(httptest.NewRecorder())
	assert.False(t, state.Committed(), "should not be committed before WriteHeader")
	w.WriteHeader(http.StatusOK)
	assert.True(t, state.Committed(), "should be committed after WriteHeader")
}

func TestRecorder_CommittedByWrite(t *testing.T) {
	state, w := NewRecorder(httptest.NewRecorder())
	assert.False(t, state.Committed())
	_, _ = w.Write([]byte("data"))
	assert.True(t, state.Committed(), "Write triggers implicit WriteHeader(200)")
}

func TestRecorder_1xxDoesNotCommit(t *testing.T) {
	state, w := NewRecorder(httptest.NewRecorder())

	w.WriteHeader(http.StatusContinue) // 100
	assert.False(t, state.Committed(), "1xx must not mark committed")
	assert.Equal(t, http.StatusOK, state.Status(), "status must remain default 200")

	w.WriteHeader(http.StatusOK) // 200 — the real response
	assert.True(t, state.Committed())
	assert.Equal(t, http.StatusOK, state.Status())
}

func TestRecorder_DuplicateWriteHeaderSuppressed(t *testing.T) {
	rec := httptest.NewRecorder()
	state, w := NewRecorder(rec)

	w.WriteHeader(http.StatusCreated)
	w.WriteHeader(http.StatusNotFound) // should be suppressed

	assert.Equal(t, http.StatusCreated, state.Status())
	assert.Equal(t, http.StatusCreated, rec.Code)
}

// hijackWriter implements http.Hijacker for testing.
type hijackWriter struct {
	http.ResponseWriter
}

func (h *hijackWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, errors.New("hijack mock")
}

func TestRecorder_PreservesHijacker(t *testing.T) {
	hw := &hijackWriter{ResponseWriter: httptest.NewRecorder()}
	_, w := NewRecorder(hw)

	// The handler receives w as http.ResponseWriter — verify it preserves Hijacker.
	hijacker, ok := w.(http.Hijacker)
	require.True(t, ok, "wrapped writer must preserve http.Hijacker")

	_, _, err := hijacker.Hijack()
	assert.EqualError(t, err, "hijack mock")
}

// flusherWriter implements http.Flusher for testing.
type flusherWriter struct {
	http.ResponseWriter
	flushed bool
}

func (f *flusherWriter) Flush() { f.flushed = true }

func TestRecorder_PreservesFlusher(t *testing.T) {
	fw := &flusherWriter{ResponseWriter: httptest.NewRecorder()}
	state, w := NewRecorder(fw)

	flusher, ok := w.(http.Flusher)
	require.True(t, ok, "wrapped writer must preserve http.Flusher")

	flusher.Flush()
	assert.True(t, fw.flushed, "Flush must reach underlying writer")
	assert.False(t, state.Committed(), "Flush alone must not mark committed")
}

func TestRecorder_FlushAfter1xxDoesNotCommit(t *testing.T) {
	fw := &flusherWriter{ResponseWriter: httptest.NewRecorder()}
	state, w := NewRecorder(fw)

	w.WriteHeader(http.StatusEarlyHints) // 103
	w.(http.Flusher).Flush()

	assert.False(t, state.Committed(), "Flush after 1xx must not mark committed")

	w.WriteHeader(http.StatusOK) // final response
	assert.True(t, state.Committed())
}

// readerFromWriter implements http.ResponseWriter + io.ReaderFrom for testing.
type readerFromWriter struct {
	http.ResponseWriter
	readFromBytes int64
}

func (r *readerFromWriter) ReadFrom(src io.Reader) (int64, error) {
	n, err := io.Copy(r.ResponseWriter, src)
	r.readFromBytes = n
	return n, err
}

func TestRecorder_ReadFromTracksBytes(t *testing.T) {
	rfw := &readerFromWriter{ResponseWriter: httptest.NewRecorder()}
	state, w := NewRecorder(rfw)

	rf, ok := w.(io.ReaderFrom)
	require.True(t, ok, "wrapped writer must preserve io.ReaderFrom")

	n, err := rf.ReadFrom(strings.NewReader("hello world"))
	require.NoError(t, err)
	assert.Equal(t, int64(11), n)
	assert.Equal(t, int64(11), state.BytesWritten())
	assert.True(t, state.Committed())
}

func TestRecorderStateFrom_NilWhenMissing(t *testing.T) {
	assert.Nil(t, RecorderStateFrom(context.Background()))
}

func TestRecorderState_ContextRoundTrip(t *testing.T) {
	state := &RecorderState{status: 201}
	ctx := WithRecorderState(context.Background(), state)

	got := RecorderStateFrom(ctx)
	require.NotNil(t, got)
	assert.Equal(t, 201, got.Status())
}
