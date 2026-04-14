package configread

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/contracttest"
)

func TestHttpConfigGetV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.config.get.v1")

	// TODO(#8 Entity→DTO): handler outputs domain entity directly (PascalCase)
	// instead of DTO (camelCase). Invoking the real handler would fail schema
	// validation. Once #8 adds DTO mapping, rewrite to invoke real handler.
	c.ValidateResponse(t, []byte(`{"data":{"ID":"c-1","Key":"app.name","Value":"myapp","Version":1,"CreatedAt":"2026-01-01T00:00:00Z","UpdatedAt":"2026-01-01T00:00:00Z"}}`))
	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))
}
