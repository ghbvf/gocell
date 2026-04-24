package setup_test

import (
	"testing"

	"github.com/ghbvf/gocell/pkg/contracttest"
)

func TestHttpAuthSetupStatusV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.auth.setup.status.v1")

	c.ValidateResponse(t, []byte(`{"data":{"hasAdmin":false}}`))
	c.ValidateResponse(t, []byte(`{"data":{"hasAdmin":true}}`))
	// additionalProperties:false — unknown fields rejected.
	c.MustRejectResponse(t, []byte(`{"data":{"hasAdmin":false,"unexpected":"x"}}`))
	// required hasAdmin
	c.MustRejectResponse(t, []byte(`{"data":{}}`))
}

func TestHttpAuthSetupAdminV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.auth.setup.admin.v1")

	c.ValidateRequest(t, []byte(`{"username":"root","email":"root@local","password":"SecretPass!23"}`))
	c.ValidateResponse(t, []byte(`{"data":{"id":"usr-123","username":"root","email":"root@local","createdAt":"2026-04-24T12:00:00Z"}}`))

	// Missing required fields → reject
	c.MustRejectRequest(t, []byte(`{"username":"root","email":"root@local"}`))
	// Unknown fields → reject
	c.MustRejectRequest(t, []byte(`{"username":"root","email":"root@local","password":"p","extra":"field"}`))
	// Empty strings → reject (minLength:1)
	c.MustRejectRequest(t, []byte(`{"username":"","email":"root@local","password":"p"}`))
}
