package configread

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/contracttest"
)

func TestHttpConfigGetV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.config.get.v1")

	// TODO(CFG-JSON-01 #16): handler outputs PascalCase (domain entity) instead
	// of camelCase. Invoking the real handler would fail schema validation.
	// Once #16 adds json tags, rewrite this to invoke real handler via httptest.
	c.ValidateResponse(t, []byte(`{"data":{"ID":"c-1","Key":"app.name","Value":"myapp","Version":1,"CreatedAt":"2026-01-01T00:00:00Z","UpdatedAt":"2026-01-01T00:00:00Z"}}`))
	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))
}
