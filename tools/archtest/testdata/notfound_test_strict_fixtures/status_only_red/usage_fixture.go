// Package status_only_red proves that a _NotFound HTTP handler test asserting
// only http.StatusNotFound (no errcodetest.AssertWireCode call) is rejected.
// Line 17 is flagged.
package status_only_red

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHandler_HandleGet_NotFound(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.Code = http.StatusNotFound
	assert.Equal(t, http.StatusNotFound, rec.Code)
}
