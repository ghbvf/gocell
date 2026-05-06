package devicelist

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/mem"
	listcontract "github.com/ghbvf/gocell/generated/contracts/http/device/list/v1"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
)

func newTestCodec(t *testing.T) *query.CursorCodec {
	t.Helper()
	codec, err := query.NewCursorCodec([]byte("gocell-demo-DEVICE-CELL-key-32!!"))
	if err != nil {
		t.Fatal(err)
	}
	return codec
}

func seedDevice(t *testing.T, repo *mem.DeviceRepository, id, name, status string) {
	t.Helper()
	err := repo.Create(context.Background(), &domain.Device{
		ID:       id,
		Name:     name,
		Status:   status,
		LastSeen: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNewService_NilCodecReturnsError(t *testing.T) {
	_, err := NewService(mem.NewDeviceRepository(), nil, slog.Default(), query.RunModeDemo)
	if err == nil {
		t.Fatal("expected error for nil codec")
	}
	var ecErr *errcode.Error
	if !errors.As(err, &ecErr) {
		t.Errorf("expected errcode.Error, got %T: %v", err, err)
	}
}

func TestService_ListEmpty(t *testing.T) {
	svc, err := NewService(mem.NewDeviceRepository(), newTestCodec(t), slog.Default(), query.RunModeDemo)
	if err != nil {
		t.Fatal(err)
	}

	raw, err := svc.List(context.Background(), &listcontract.Request{Limit: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp := raw.(listcontract.List200JSONResponse)
	if resp.HasMore {
		t.Error("expected HasMore=false for empty repo")
	}
	if len(resp.Data) != 0 {
		t.Errorf("expected 0 items, got %d", len(resp.Data))
	}
}

func TestService_ListPagination(t *testing.T) {
	repo := mem.NewDeviceRepository()
	seedDevice(t, repo, "dev-1", "alpha", "online")
	seedDevice(t, repo, "dev-2", "beta", "offline")
	seedDevice(t, repo, "dev-3", "gamma", "online")

	svc, err := NewService(repo, newTestCodec(t), slog.Default(), query.RunModeDemo)
	if err != nil {
		t.Fatal(err)
	}

	raw1, err := svc.List(context.Background(), &listcontract.Request{Limit: 2})
	if err != nil {
		t.Fatalf("page 1 error: %v", err)
	}
	page1 := raw1.(listcontract.List200JSONResponse)
	if !page1.HasMore {
		t.Error("expected HasMore=true for first page")
	}
	if len(page1.Data) != 2 {
		t.Errorf("expected 2 items, got %d", len(page1.Data))
	}
	if page1.NextCursor == "" {
		t.Error("expected non-empty NextCursor")
	}

	raw2, err := svc.List(context.Background(), &listcontract.Request{Limit: 2, Cursor: page1.NextCursor})
	if err != nil {
		t.Fatalf("page 2 error: %v", err)
	}
	page2 := raw2.(listcontract.List200JSONResponse)
	if page2.HasMore {
		t.Error("expected HasMore=false for last page")
	}
	if page2.NextCursor != "" {
		t.Errorf("expected empty NextCursor on last page, got %q", page2.NextCursor)
	}
	if len(page2.Data) != 1 {
		t.Errorf("expected 1 item on last page, got %d", len(page2.Data))
	}
}

func TestService_ListSingleDevice(t *testing.T) {
	repo := mem.NewDeviceRepository()
	seedDevice(t, repo, "dev-1", "solo", "online")

	svc, err := NewService(repo, newTestCodec(t), slog.Default(), query.RunModeDemo)
	if err != nil {
		t.Fatal(err)
	}

	raw, err := svc.List(context.Background(), &listcontract.Request{Limit: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp := raw.(listcontract.List200JSONResponse)
	if resp.HasMore {
		t.Error("expected HasMore=false for single device")
	}
	if resp.NextCursor != "" {
		t.Errorf("expected empty NextCursor when HasMore=false, got %q", resp.NextCursor)
	}
	if len(resp.Data) != 1 {
		t.Errorf("expected 1 item, got %d", len(resp.Data))
	}
}

func TestService_ListLimitOne(t *testing.T) {
	repo := mem.NewDeviceRepository()
	seedDevice(t, repo, "dev-1", "alpha", "online")
	seedDevice(t, repo, "dev-2", "beta", "offline")

	svc, err := NewService(repo, newTestCodec(t), slog.Default(), query.RunModeDemo)
	if err != nil {
		t.Fatal(err)
	}

	raw, err := svc.List(context.Background(), &listcontract.Request{Limit: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp := raw.(listcontract.List200JSONResponse)
	if !resp.HasMore {
		t.Error("expected HasMore=true with limit=1 and 2 devices")
	}
	if len(resp.Data) != 1 {
		t.Errorf("expected 1 item, got %d", len(resp.Data))
	}
	if resp.Data[0].Name != "alpha" {
		t.Errorf("expected first item to be 'alpha' (name ASC), got %q", resp.Data[0].Name)
	}
}

func TestService_ListSecondarySort(t *testing.T) {
	repo := mem.NewDeviceRepository()
	seedDevice(t, repo, "dev-z", "sensor", "offline")
	seedDevice(t, repo, "dev-a", "sensor", "online")

	svc, err := NewService(repo, newTestCodec(t), slog.Default(), query.RunModeDemo)
	if err != nil {
		t.Fatal(err)
	}

	raw, err := svc.List(context.Background(), &listcontract.Request{Limit: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp := raw.(listcontract.List200JSONResponse)
	if len(resp.Data) != 2 {
		t.Fatalf("expected 2 items, got %d", len(resp.Data))
	}
	if resp.Data[0].ID != "dev-a" {
		t.Errorf("expected first item id='dev-a' (id ASC), got %q", resp.Data[0].ID)
	}
	if resp.Data[1].ID != "dev-z" {
		t.Errorf("expected second item id='dev-z', got %q", resp.Data[1].ID)
	}
}
