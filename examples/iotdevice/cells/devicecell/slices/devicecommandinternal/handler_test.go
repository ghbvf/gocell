package devicecommandinternal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/devicecmd"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/mem"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/command"
	"github.com/ghbvf/gocell/kernel/command/commandtest"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
)

// Compile-time check: the type alias is transparent.
var _ *devicecmd.Service = (*Service)(nil)

func setupInternalHandler() (*Handler, *commandtest.InMemQueue) {
	devRepo := mem.NewDeviceRepository()
	q := commandtest.NewInMemQueue()
	codec, _ := query.NewCursorCodec(bytes.Repeat([]byte("k"), 32))
	svc, err := devicecmd.NewService(
		q, devRepo, codec, slog.Default(), query.RunModeProd,
		devicecmd.WithClock(clock.Real()),
		devicecmd.WithSliceName("devicecommandinternal"),
	)
	if err != nil {
		panic(err)
	}
	_ = devRepo.Create(context.Background(), &domain.Device{
		ID: "dev-1", Name: "sensor-a", Status: "online",
	})
	return NewHandler(svc), q
}

func TestHandleScanActive(t *testing.T) {
	intH, q := setupInternalHandler()
	ctx := context.Background()
	now := time.Now()
	// Seed 3 commands so pagination cursor logic is exercised.
	for i := range 3 {
		id := fmt.Sprintf("cmd-scan-%d", i)
		entry := command.NewEntry(id, "dev-1", "reboot", []byte("payload"),
			command.Timeouts{}, now.Add(time.Duration(i)*time.Second))
		require.NoError(t, q.Enqueue(ctx, entry, command.EnqueueOptions{}))
	}

	mux := celltest.NewTestMux()
	require.NoError(t, intH.RegisterRoutes(mux))

	// Fetch first page.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/devicecommands?limit=2", nil)
	req = req.WithContext(auth.TestServiceContext("devicecell"))
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data, ok := resp["data"].([]any)
	require.True(t, ok, "response should have data array")
	assert.Len(t, data, 2)
	assert.Equal(t, true, resp["hasMore"])
	assert.NotEmpty(t, resp["nextCursor"])

	// Fetch second page using cursor.
	cursor := resp["nextCursor"].(string)
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/internal/v1/devicecommands?limit=2&cursor="+cursor, nil)
	req2 = req2.WithContext(auth.TestServiceContext("devicecell"))
	mux.ServeHTTP(w2, req2)

	require.Equal(t, http.StatusOK, w2.Code)
	var resp2 map[string]any
	require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &resp2))
	data2, ok := resp2["data"].([]any)
	require.True(t, ok)
	assert.Len(t, data2, 1)
	assert.Equal(t, false, resp2["hasMore"])
}

func TestHandleScanActive_InvalidCursor(t *testing.T) {
	devRepo := mem.NewDeviceRepository()
	q := commandtest.NewInMemQueue()
	codec, _ := query.NewCursorCodec(bytes.Repeat([]byte("k"), 32))
	svc, err := devicecmd.NewService(
		q, devRepo, codec, slog.Default(), query.RunModeProd,
		devicecmd.WithClock(clock.Real()),
		devicecmd.WithSliceName("devicecommandinternal"),
	)
	require.NoError(t, err)

	wrongSort := []query.SortColumn{{Name: "other", Direction: query.SortASC}, {Name: "x", Direction: query.SortASC}}
	missingFieldsToken, _ := codec.Encode(query.Cursor{Values: []any{"v1", "v2"}})
	crossContextToken, _ := codec.Encode(query.Cursor{
		Values:  []any{"v1", "v2"},
		Scope:   query.SortScope(wrongSort),
		Context: query.QueryContext("endpoint", "wrong-endpoint"),
	})

	tests := []struct {
		name   string
		cursor string
	}{
		{"garbage token", "not-a-valid-cursor!!!"},
		{"missing scope and context", missingFieldsToken},
		{"cross-context replay", crossContextToken},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			intH := NewHandler(svc)
			mux := celltest.NewTestMux()
			if err := intH.RegisterRoutes(mux); err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/internal/v1/devicecommands?cursor="+tc.cursor, nil)
			req = req.WithContext(auth.TestServiceContext("devicecell"))
			mux.ServeHTTP(w, req)

			assert.Equal(t, http.StatusBadRequest, w.Code)
			assert.Contains(t, w.Body.String(), "ERR_CURSOR_INVALID")
		})
	}
}

func TestParseStatusFilter(t *testing.T) {
	// Verify status filter handling indirectly via a List call that returns results.
	intH, q := setupInternalHandler()
	ctx := context.Background()
	now := time.Now()

	entry := command.NewEntry("cmd-pending", "dev-1", "reboot", []byte("x"), command.Timeouts{}, now)
	require.NoError(t, q.Enqueue(ctx, entry, command.EnqueueOptions{}))

	mux := celltest.NewTestMux()
	require.NoError(t, intH.RegisterRoutes(mux))

	// Filter by pending status — should return 1 result.
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/devicecommands?statuses=pending", nil)
	req = req.WithContext(auth.TestServiceContext("devicecell"))
	mux.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	data, ok := resp["data"].([]any)
	require.True(t, ok)
	assert.Len(t, data, 1)

	// Filter by invalid status — should return 400.
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/internal/v1/devicecommands?statuses=unknown", nil)
	req2 = req2.WithContext(auth.TestServiceContext("devicecell"))
	mux.ServeHTTP(w2, req2)

	assert.Equal(t, http.StatusBadRequest, w2.Code)
}
