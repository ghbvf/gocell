package setup_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/cells/accesscore/slices/setup"
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

	// Also exercise the real handler and feed its recorded output through the
	// contract validator so serialisation bugs would surface.
	svc := newService(t, mem.NewUserRepository(), mem.NewRoleRepository(), nil)
	h := setup.NewHandler(svc)
	req := httptest.NewRequest(http.MethodGet, c.HTTP.Path, nil)
	rec := httptest.NewRecorder()
	h.HandleStatus(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)
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
	// Password too short → reject (minLength:8)
	c.MustRejectRequest(t, []byte(`{"username":"u","email":"u@x","password":"short"}`))

	// Real-handler produced 201 payload must satisfy the response schema.
	svc := newService(t, mem.NewUserRepository(), mem.NewRoleRepository(), &stubWriter{})
	h := setup.NewHandler(svc)
	body := `{"username":"root","email":"root@local","password":"SecretPass!23"}`
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.HandleCreateAdmin(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code)
	c.ValidateHTTPResponseRecorder(t, rec)
}
