// Package compliant_wire_green proves that an HTTP handler-style FuncDecl
// calling errcodetest.AssertWireCode is accepted: 0 violations expected.
package compliant_wire_green

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/errcode/errcodetest"
)

func TestHandler_HandleGet_NotFound(t *testing.T) {
	rec := httptest.NewRecorder()
	rec.Code = http.StatusNotFound
	rec.Body.WriteString(`{"error":{"code":"ERR_CONFIG_NOT_FOUND","message":"x","details":[]}}`)
	errcodetest.AssertWireCode(t, rec, http.StatusNotFound, errcode.ErrConfigNotFound)
}
