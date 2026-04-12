package configpublish

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/config-core/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Contract: event.config.changed.v1 — publish action publishes {action, key, config_id, version}.
func TestEventConfigChangedV1Publish(t *testing.T) {
	handler, repo := setupHandler()
	now := time.Now()
	require.NoError(t, repo.Create(context.Background(), &domain.ConfigEntry{
		ID: "cfg-pub-1", Key: "app.name", Value: "gocell", Version: 1,
		CreatedAt: now, UpdatedAt: now,
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/app.name/publish", nil)
	handler.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code,
		"contract: publish must succeed, triggering event.config.changed.v1")
}

// Contract: event.config.rollback.v1 — rollback publishes {action, key, target_version, new_version}.
func TestEventConfigRollbackV1Publish(t *testing.T) {
	handler, repo := setupHandler()
	now := time.Now()
	pAt := now
	require.NoError(t, repo.Create(context.Background(), &domain.ConfigEntry{
		ID: "cfg-rb-1", Key: "app.name", Value: "v1", Version: 2,
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, repo.PublishVersion(context.Background(), &domain.ConfigVersion{
		ConfigID:    "cfg-rb-1",
		Version:     1,
		Value:       "v0",
		PublishedAt: &pAt,
	}))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/app.name/rollback",
		strings.NewReader(`{"version":1}`))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Contains(t, resp, "data", "contract requires data envelope")
}
