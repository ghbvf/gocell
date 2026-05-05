package setup_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/cells/accesscore/slices/setup"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/tests/contracttest"
)

func TestHttpAuthSetupStatusV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.auth.setup.status.v1")

	c.ValidateResponse(t, []byte(`{"data":{"hasAdmin":false}}`))
	c.ValidateResponse(t, []byte(`{"data":{"hasAdmin":true}}`))
	// Per ADR-202605031600, response schema is lenient — unknown fields accepted.
	c.ValidateResponse(t, []byte(`{"data":{"hasAdmin":false,"unexpected":"x"}}`))
	// required hasAdmin still enforced (structural, not strict-AP).
	c.MustRejectResponse(t, []byte(`{"data":{}}`))

	// Also exercise the real handler and feed its recorded output through the
	// contract validator so serialization bugs would surface.
	svc := newService(t, mem.NewUserRepository(), mem.NewRoleRepository(), nil)
	h := setup.NewHandler(svc, testPassthroughAuth)
	req := httptest.NewRequest(http.MethodGet, c.HTTP.Path, nil)
	rec := httptest.NewRecorder()
	newHandlerMux(t, h).ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)

	t.Run("500 provisioner failure response satisfies contract", func(t *testing.T) {
		// status endpoint 5xx contract envelope 校验：handler 在 provisioner
		// 故障时返回 500，body 走标准 error envelope。同步断言 5xx body 不泄漏
		// 内部细节（pkg/httputil 的 5xx 路径会清空 details — 本断言把这条不变量
		// 沉淀为防退化测试）。Service 构造细节封装在 newServiceWithProvisionerError
		// (service_test.go)，避免 contract 层依赖 service-internal 的 repo 选型。
		svc := newServiceWithProvisionerError(t,
			errors.New("provisioner status: pg unreachable"))
		h := setup.NewHandler(svc, testPassthroughAuth)

		req := httptest.NewRequest(http.MethodGet, c.HTTP.Path, nil)
		rec := httptest.NewRecorder()
		newHandlerMux(t, h).ServeHTTP(rec, req)

		require.Equal(t, http.StatusInternalServerError, rec.Code)
		c.ValidateErrorResponse(t, http.StatusInternalServerError, rec.Body.Bytes())

		bodyStr := rec.Body.String()
		assert.NotContains(t, bodyStr, "pg unreachable",
			"5xx body must not leak underlying infra error message")
		assert.NotContains(t, bodyStr, "loginEndpoint",
			"5xx body must not carry retired loginEndpoint key")
		assert.Contains(t, bodyStr, `"details":[]`,
			"5xx envelope must clear details — pkg/httputil pins this invariant")
	})
}

func TestHttpAuthSetupAdminV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot(t)
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
	c.MustRejectRequest(t, []byte(`{"username":"root","email":"root@local","password":"`+strings.Repeat("界", 8)+`"}`))

	// Real-handler produced 201 payload must satisfy the response schema.
	svc := newService(t, mem.NewUserRepository(), mem.NewRoleRepository(), &stubWriter{})
	h := setup.NewHandler(svc, testPassthroughAuth)
	body := `{"username":"root","email":"root@local","password":"SecretPass!23"}`
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	newHandlerMux(t, h).ServeHTTP(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code)
	c.ValidateHTTPResponseRecorder(t, rec)

	// Real-handler negative contract checks: requests rejected by the schema
	// must be rejected by the public endpoint before persistence.
	for _, badBody := range []string{
		`{"username":"` + strings.Repeat("u", 129) + `","email":"root@local","password":"SecretPass!23"}`,
		`{"username":"root","email":"` + strings.Repeat("e", 257) + `","password":"SecretPass!23"}`,
		`{"username":"root","email":"root@local","password":"` + strings.Repeat("p", 73) + `"}`,
		`{"username":"root","email":"root@local","password":"` + strings.Repeat("界", 8) + `"}`,
	} {
		svc := newService(t, mem.NewUserRepository(), mem.NewRoleRepository(), &stubWriter{})
		h := setup.NewHandler(svc, testPassthroughAuth)
		req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, strings.NewReader(badBody))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		newHandlerMux(t, h).ServeHTTP(rec, req)
		require.Equal(t, http.StatusBadRequest, rec.Code)
	}

	t.Run("409 duplicate identity user response satisfies contract", func(t *testing.T) {
		userRepo := mem.NewUserRepository()
		roleRepo := mem.NewRoleRepository()
		seedContractIdentityUser(t, userRepo, "root", "root@local")
		svc := newService(t, userRepo, roleRepo, &stubWriter{})
		h := setup.NewHandler(svc, testPassthroughAuth)

		req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		newHandlerMux(t, h).ServeHTTP(rec, req)

		require.Equal(t, http.StatusConflict, rec.Code)
		c.ValidateErrorResponse(t, http.StatusConflict, rec.Body.Bytes())
		requireErrorCode(t, rec.Body.Bytes(), errcode.ErrAuthUserDuplicate)
	})

	t.Run("409 bootstrap-pending duplicate response satisfies contract", func(t *testing.T) {
		userRepo := mem.NewUserRepository()
		roleRepo := mem.NewRoleRepository()
		orphan, err := domain.NewUser("root", "root@local", "$2a$10$oldhash00000000000000000000000000000000000000000000000", time.Now())
		require.NoError(t, err)
		orphan.ID = "usr-bootstrap-prior"
		orphan.MarkProvisionPending(domain.UserSourceBootstrap, time.Now())
		require.NoError(t, userRepo.Create(context.Background(), orphan))
		svc := newService(t, userRepo, roleRepo, &stubWriter{})
		h := setup.NewHandler(svc, testPassthroughAuth)

		req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		newHandlerMux(t, h).ServeHTTP(rec, req)

		require.Equal(t, http.StatusConflict, rec.Code)
		c.ValidateErrorResponse(t, http.StatusConflict, rec.Body.Bytes())
		requireErrorCode(t, rec.Body.Bytes(), errcode.ErrAuthUserDuplicate)
	})

	t.Run("410 retired endpoint response satisfies contract", func(t *testing.T) {
		// PR-A42 N5 / N4: pin the retired-endpoint envelope through the contract
		// validator and assert the wire shape carries semantic next-action only —
		// no HTTP path literal (clients resolve via OpenAPI).
		userRepo := mem.NewUserRepository()
		roleRepo := mem.NewRoleRepository()
		seedAdmin(t, userRepo, roleRepo)
		svc := newService(t, userRepo, roleRepo, &stubWriter{})
		h := setup.NewHandler(svc, testPassthroughAuth)

		req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		newHandlerMux(t, h).ServeHTTP(rec, req)

		require.Equal(t, http.StatusGone, rec.Code)
		c.ValidateErrorResponse(t, http.StatusGone, rec.Body.Bytes())
		requireErrorCode(t, rec.Body.Bytes(), errcode.ErrSetupAlreadyInitialized)

		bodyStr := rec.Body.String()
		assert.Contains(t, bodyStr, `"key":"nextAction","value":"login"`)
		assert.NotContains(t, bodyStr, "/api/", "410 wire shape must not leak path literals")
		assert.NotContains(t, bodyStr, "loginEndpoint", "loginEndpoint key retired by PR-A42")
	})
}

// TestEventUserCreatedV1Publish_FromSetup closes the slice.yaml
// contract.event.user.created.v1.publish verification gap: the setup slice
// declares it publishes event.user.created.v1, so this test must drive the
// real handler path and assert the emitted outbox entry's payload + headers
// both satisfy the event contract.
func TestEventUserCreatedV1Publish_FromSetup(t *testing.T) {
	root := contracttest.ContractsRoot(t)
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

func seedContractIdentityUser(t *testing.T, userRepo *mem.UserRepository, username, email string) {
	t.Helper()
	u, err := domain.NewUser(username, email, "$2a$10$stubhashXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX", time.Now())
	require.NoError(t, err)
	u.ID = "usr-existing"
	require.NoError(t, userRepo.Create(context.Background(), u))
}

func requireErrorCode(t *testing.T, body []byte, code errcode.Code) {
	t.Helper()
	var resp struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(body, &resp))
	require.Equal(t, string(code), resp.Error.Code)
}
