package configread

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/ghbvf/gocell/cells/config-core/internal/mem"
	"github.com/ghbvf/gocell/pkg/contracttest"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/stretchr/testify/require"
)

func newContractHandler() (http.Handler, *mem.ConfigRepository) {
	repo := mem.NewConfigRepository()
	codec, _ := query.NewCursorCodec([]byte("gocell-demo-cursor-key-32bytes!!"))
	svc := NewService(repo, codec, slog.Default())
	h := NewHandler(svc)
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/config/{key}", http.HandlerFunc(h.HandleGet))
	return mux, repo
}

func TestHttpConfigGetV1Serve(t *testing.T) {
	root := contracttest.ContractsRoot()
	c := contracttest.LoadByID(t, root, "http.config.get.v1")
	handler, repo := newContractHandler()

	now := time.Now()
	require.NoError(t, repo.Create(context.Background(), &domain.ConfigEntry{
		ID: "cfg-1", Key: "app.name", Value: "gocell", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}))

	path := strings.Replace(c.HTTP.Path, "{key}", "app.name", 1)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, path, nil)
	handler.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)

	c.MustRejectResponse(t, []byte(`{"wrong":"shape"}`))
}
