package configread

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/contracttest"
)

func TestHttpConfigGetV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.config.get.v1")

	// NOTE: config.get response schema uses PascalCase (known issue CFG-JSON-01).
	c.ValidateResponse(t, []byte(`{"data":{"ID":"c-1","Key":"app.name","Value":"myapp","Version":1,"CreatedAt":"2026-01-01T00:00:00Z","UpdatedAt":"2026-01-01T00:00:00Z"}}`))
}
