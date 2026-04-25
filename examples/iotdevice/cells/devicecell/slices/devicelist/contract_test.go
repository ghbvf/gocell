package devicelist

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/mem"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/cell/celltest"
	"github.com/ghbvf/gocell/pkg/contracttest"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
)

func newContractDeviceListHandler(t *testing.T) http.Handler {
	t.Helper()
	repo := mem.NewDeviceRepository()
	now := time.Now()
	_ = repo.Create(context.Background(), &domain.Device{
		ID: "dev-1", Name: "sensor-alpha", Status: "online", LastSeen: now,
	})
	_ = repo.Create(context.Background(), &domain.Device{
		ID: "dev-2", Name: "sensor-beta", Status: "offline", LastSeen: now,
	})
	_ = repo.Create(context.Background(), &domain.Device{
		ID: "dev-3", Name: "sensor-gamma", Status: "online", LastSeen: now,
	})

	codec, err := query.NewCursorCodec([]byte("gocell-demo-DEVICE-CELL-key-32!!"))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(repo, codec, slog.Default(), query.RunModeProd)
	if err != nil {
		t.Fatal(err)
	}

	mux := celltest.NewTestMux()
	mux.Route("/api/v1/devices", func(sub cell.RouteMux) { NewHandler(svc).RegisterRoutes(sub) })
	return mux
}

type deviceListPage struct {
	Data       []DeviceResponse `json:"data"`
	NextCursor string           `json:"nextCursor"`
	HasMore    bool             `json:"hasMore"`
}

func decodeDeviceListPage(t *testing.T, rec *httptest.ResponseRecorder) deviceListPage {
	t.Helper()
	var page deviceListPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode device list response: %v", err)
	}
	return page
}

func TestHttpDeviceListV1Serve(t *testing.T) {
	root := contracttest.ExampleContractsRoot("iotdevice")
	c := contracttest.LoadByID(t, root, "http.device.list.v1")
	h := newContractDeviceListHandler(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path+"?limit=2", nil)
	req = req.WithContext(auth.TestContext("user-1", []string{"admin"}))
	h.ServeHTTP(rec, req)
	c.ValidateHTTPResponseRecorder(t, rec)
	page1 := decodeDeviceListPage(t, rec)
	if len(page1.Data) != 2 {
		t.Fatalf("page 1: expected 2 devices, got %d", len(page1.Data))
	}
	if !page1.HasMore {
		t.Fatal("page 1: expected hasMore=true")
	}
	if page1.NextCursor == "" {
		t.Fatal("page 1: expected non-empty nextCursor")
	}

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path+"?limit=2&cursor="+url.QueryEscape(page1.NextCursor), nil)
	req2 = req2.WithContext(auth.TestContext("user-1", []string{"admin"}))
	h.ServeHTTP(rec2, req2)
	c.ValidateHTTPResponseRecorder(t, rec2)
	page2 := decodeDeviceListPage(t, rec2)
	if len(page2.Data) != 1 {
		t.Fatalf("page 2: expected 1 device, got %d", len(page2.Data))
	}
	if page2.HasMore {
		t.Fatal("page 2: expected hasMore=false")
	}
	if page2.NextCursor != "" {
		t.Fatalf("page 2: expected empty nextCursor, got %q", page2.NextCursor)
	}

	c.MustRejectResponse(t, []byte(`{"data":{"wrong":"shape"}}`))
	c.MustRejectResponse(t, []byte(`{"data":{"id":"dev-1"},"hasMore":false}`))
	c.MustRejectResponse(t, []byte(`{"data":[],"hasMore":"yes"}`))

	rec400 := httptest.NewRecorder()
	req400 := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path+"?limit=notanumber", nil)
	req400 = req400.WithContext(auth.TestContext("user-1", []string{"admin"}))
	h.ServeHTTP(rec400, req400)
	if rec400.Code != http.StatusBadRequest {
		t.Errorf("invalid limit: expected 400, got %d", rec400.Code)
	}
	c.ValidateErrorResponse(t, http.StatusBadRequest, rec400.Body.Bytes())

	recBadCursor := httptest.NewRecorder()
	reqBadCursor := httptest.NewRequest(c.HTTP.Method, c.HTTP.Path+"?cursor=not-a-valid-cursor", nil)
	reqBadCursor = reqBadCursor.WithContext(auth.TestContext("user-1", []string{"admin"}))
	h.ServeHTTP(recBadCursor, reqBadCursor)
	if recBadCursor.Code != http.StatusBadRequest {
		t.Errorf("invalid cursor: expected 400, got %d", recBadCursor.Code)
	}
	c.ValidateErrorResponse(t, http.StatusBadRequest, recBadCursor.Body.Bytes())
}

func TestDeviceListContractSpecMatchesContract(t *testing.T) {
	root := contracttest.ExampleContractsRoot("iotdevice")
	c := contracttest.LoadByID(t, root, "http.device.list.v1")
	if c.HTTP == nil {
		t.Fatal("http.device.list.v1 must declare HTTP transport metadata")
	}
	if specDeviceListSlice.ID != c.ID {
		t.Fatalf("ContractSpec ID = %q, want %q", specDeviceListSlice.ID, c.ID)
	}
	if specDeviceListSlice.Method != c.HTTP.Method {
		t.Fatalf("ContractSpec Method = %q, want %q", specDeviceListSlice.Method, c.HTTP.Method)
	}
	if specDeviceListSlice.Path != c.HTTP.Path {
		t.Fatalf("ContractSpec Path = %q, want %q", specDeviceListSlice.Path, c.HTTP.Path)
	}
}
