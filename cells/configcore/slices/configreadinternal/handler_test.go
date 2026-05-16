package configreadinternal

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/configcore/internal/configreader"
	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/cells/configcore/internal/dto"
	"github.com/ghbvf/gocell/cells/configcore/internal/mem"
	internalapig "github.com/ghbvf/gocell/generated/contracts/http/config/internalapi/get/v1"
	kcell "github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/errcode/errcodetest"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
)

const internalBasePath = "/internal/v1/config"

// asCaller attaches an accesscore service principal so the request satisfies
// the RequireCallerCell("accesscore") policy applied by RegisterRoutes.
func asCaller(req *http.Request) *http.Request {
	return req.WithContext(auth.TestServiceContext("accesscore"))
}

// setupHandler wires the internal slice handler onto a celltest mux via
// RegisterRoutes — nested under /internal/v1/config to mirror the production
// InternalListener layout in cell_gen.go.
func setupHandler() (http.Handler, *mem.ConfigRepository) {
	repo := mem.NewConfigRepository(clock.Real())
	codec, _ := query.NewCursorCodec([]byte("gocell-demo-cursor-key-32bytes!!"))
	svc, err := configreader.NewService(repo, codec, slog.Default(), "configreadinternal", query.RunModeProd)
	if err != nil {
		panic(err)
	}
	mux := celltest.NewTestMux()
	mux.Route(internalBasePath, func(sub kcell.RouteMux) {
		if err := NewHandler(svc).RegisterRoutes(sub); err != nil {
			panic("RegisterRoutes: " + err.Error())
		}
	})
	return mux, repo
}

// TestHandler_InternalGet_NilPrincipal_401 asserts that a request without a
// Principal (i.e. the listener auth chain did not inject one) is rejected with
// 401 ERR_AUTH_UNAUTHORIZED before reaching any business logic.
func TestHandler_InternalGet_NilPrincipal_401(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, internalBasePath+"/some-key", nil)
	// Intentionally not calling asCaller — no principal in context.
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
	var resp struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "ERR_AUTH_UNAUTHORIZED", resp.Error.Code)
}

// TestHandler_InternalGet_WrongCaller_403 asserts that a service principal
// whose callerCell is not in the RequireCallerCell("accesscore") allowlist is
// rejected with 403 ERR_AUTH_FORBIDDEN.
func TestHandler_InternalGet_WrongCaller_403(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, internalBasePath+"/some-key", nil)
	// auditcore is a valid caller cell but not in the accesscore allowlist.
	req = req.WithContext(auth.TestServiceContext("auditcore"))
	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusForbidden, w.Code)
	var resp struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "ERR_AUTH_FORBIDDEN", resp.Error.Code)
}

func TestHandler_InternalGet_Found(t *testing.T) {
	handler, repo := setupHandler()
	now := time.Now()
	require.NoError(t, repo.Create(context.Background(), &domain.ConfigEntry{
		ID: "cfg-1", Key: "app.name", Value: "gocell", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, internalBasePath+"/app.name", nil)
	handler.ServeHTTP(w, asCaller(req))

	assert.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data internalapig.ResponseData `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "app.name", resp.Data.Key)
	assert.Equal(t, "gocell", resp.Data.Value)
}

func TestHandler_InternalGet_NotFound(t *testing.T) {
	handler, _ := setupHandler()

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, internalBasePath+"/missing-key", nil)
	handler.ServeHTTP(w, asCaller(req))

	errcodetest.AssertWireCode(t, w, http.StatusNotFound, errcode.ErrConfigNotFound)
}

func TestHandler_InternalGet_SensitiveRedacted(t *testing.T) {
	handler, repo := setupHandler()
	now := time.Now()
	require.NoError(t, repo.Create(context.Background(), &domain.ConfigEntry{
		ID: "cfg-s1", Key: "db.password", Value: "s3cret!", Sensitive: true,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, internalBasePath+"/db.password", nil)
	handler.ServeHTTP(w, asCaller(req))

	assert.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Data internalapig.ResponseData `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, dto.RedactedValue, resp.Data.Value)
	assert.True(t, resp.Data.Sensitive)
	assert.NotContains(t, w.Body.String(), "s3cret!")
}
