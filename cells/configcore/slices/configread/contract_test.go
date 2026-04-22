package configread

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/contracttest"
)

func TestHttpConfigGetV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.config.get.v1")

	c.ValidateResponse(t, []byte(`{"data":{"id":"c-1","key":"app.name","value":"myapp","version":1,"createdAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-01T00:00:00Z"}}`))
	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))
}
