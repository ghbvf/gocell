package middleware

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecorder_CapturesStatus(t *testing.T) {
	rec := NewRecorder(httptest.NewRecorder())
	rec.WriteHeader(http.StatusNotFound)
	assert.Equal(t, http.StatusNotFound, rec.Status())
}

func TestRecorder_DefaultStatus200(t *testing.T) {
	w := httptest.NewRecorder()
	rec := NewRecorder(w)
	_, _ = rec.Write([]byte("hello"))
	assert.Equal(t, http.StatusOK, rec.Status())
}

func TestRecorder_BytesWritten(t *testing.T) {
	rec := NewRecorder(httptest.NewRecorder())
	n, err := rec.Write([]byte("0123456789"))
	require.NoError(t, err)
	assert.Equal(t, 10, n)
	assert.Equal(t, int64(10), rec.BytesWritten())
}

func TestRecorder_Committed(t *testing.T) {
	rec := NewRecorder(httptest.NewRecorder())
	assert.False(t, rec.Committed(), "should not be committed before WriteHeader")
	rec.WriteHeader(http.StatusOK)
	assert.True(t, rec.Committed(), "should be committed after WriteHeader")
}

func TestRecorder_CommittedByWrite(t *testing.T) {
	rec := NewRecorder(httptest.NewRecorder())
	assert.False(t, rec.Committed())
	_, _ = rec.Write([]byte("data"))
	assert.True(t, rec.Committed(), "Write triggers implicit WriteHeader(200)")
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
	rec := NewRecorder(hw)

	// httpsnoop preserves Hijacker on the embedded ResponseWriter.
	hijacker, ok := rec.ResponseWriter.(http.Hijacker)
	require.True(t, ok, "Recorder must preserve http.Hijacker")

	_, _, err := hijacker.Hijack()
	assert.EqualError(t, err, "hijack mock")
}
