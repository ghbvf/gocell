package setup_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
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
	// Schema upper bounds must reject oversized public setup requests.
	c.MustRejectRequest(t, []byte(`{"username":"`+strings.Repeat("u", 129)+`","email":"root@local","password":"SecretPass!23"}`))
	c.MustRejectRequest(t, []byte(`{"username":"root","email":"`+strings.Repeat("e", 257)+`","password":"SecretPass!23"}`))
	c.MustRejectRequest(t, []byte(`{"username":"root","email":"root@local","password":"`+strings.Repeat("p", 73)+`"}`))

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

	// Real-handler negative contract checks: requests rejected by the schema
	// must be rejected by the public endpoint before persistence.
	for _, badBody := range []string{
		`{"username":"` + strings.Repeat("u", 129) + `","email":"root@local","password":"SecretPass!23"}`,
		`{"username":"root","email":"` + strings.Repeat("e", 257) + `","password":"SecretPass!23"}`,
		`{"username":"root","email":"root@local","password":"` + strings.Repeat("p", 73) + `"}`,
	} {
		svc := newService(t, mem.NewUserRepository(), mem.NewRoleRepository(), &stubWriter{})
		h := setup.NewHandler(svc)
		req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, strings.NewReader(badBody))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.HandleCreateAdmin(rec, req)
		require.Equal(t, http.StatusBadRequest, rec.Code)
	}
}

// TestEventUserCreatedV1Publish_FromSetup closes the slice.yaml
// contract.event.user.created.v1.publish verification gap: the setup slice
// declares it publishes event.user.created.v1, so this test must drive the
// real handler path and assert the emitted outbox entry's payload + headers
// both satisfy the event contract.
func TestEventUserCreatedV1Publish_FromSetup(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "event.user.created.v1")

	w := &stubWriter{}
	svc := newService(t, mem.NewUserRepository(), mem.NewRoleRepository(), w)

	_, err := svc.CreateAdmin(context.Background(), setup.CreateAdminInput{
		Username: "root",
		Email:    "root@local",
		Password: "SecretPass!23",
	})
	require.NoError(t, err)

	require.Len(t, w.entries, 1, "setup.CreateAdmin must emit exactly one event on fresh create")
	entry := w.entries[0]
	assert.Equal(t, dto.TopicUserCreated, entry.EventType)

	// Payload + headers must satisfy the published contract schema.
	c.ValidatePayload(t, entry.Payload)
	c.ValidateHeaders(t, []byte(`{"event_id":"`+entry.ID+`"}`))
	// Negative: schema rejects an incomplete payload.
	c.MustRejectPayload(t, []byte(`{"user_id":"x"}`))
}
