package devicelist

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/device-cell/internal/domain"
	"github.com/ghbvf/gocell/cells/device-cell/internal/mem"
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

	result, err := svc.List(context.Background(), query.PageRequest{Limit: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.HasMore {
		t.Error("expected HasMore=false for empty repo")
	}
	if len(result.Items) != 0 {
		t.Errorf("expected 0 items, got %d", len(result.Items))
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

	// First page: limit 2
	page1, err := svc.List(context.Background(), query.PageRequest{Limit: 2})
	if err != nil {
		t.Fatalf("page 1 error: %v", err)
	}
	if !page1.HasMore {
		t.Error("expected HasMore=true for first page")
	}
	if len(page1.Items) != 2 {
		t.Errorf("expected 2 items, got %d", len(page1.Items))
	}
	if page1.NextCursor == "" {
		t.Error("expected non-empty NextCursor")
	}

	// Second page using cursor
	page2, err := svc.List(context.Background(), query.PageRequest{Limit: 2, Cursor: page1.NextCursor})
	if err != nil {
		t.Fatalf("page 2 error: %v", err)
	}
	if page2.HasMore {
		t.Error("expected HasMore=false for last page")
	}
	if len(page2.Items) != 1 {
		t.Errorf("expected 1 item on last page, got %d", len(page2.Items))
	}
}

func TestService_ListSingleDevice(t *testing.T) {
	repo := mem.NewDeviceRepository()
	seedDevice(t, repo, "dev-1", "solo", "online")

	svc, err := NewService(repo, newTestCodec(t), slog.Default(), query.RunModeDemo)
	if err != nil {
		t.Fatal(err)
	}

	result, err := svc.List(context.Background(), query.PageRequest{Limit: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.HasMore {
		t.Error("expected HasMore=false for single device")
	}
	if result.NextCursor != "" {
		t.Errorf("expected empty NextCursor when HasMore=false, got %q", result.NextCursor)
	}
	if len(result.Items) != 1 {
		t.Errorf("expected 1 item, got %d", len(result.Items))
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

	result, err := svc.List(context.Background(), query.PageRequest{Limit: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.HasMore {
		t.Error("expected HasMore=true with limit=1 and 2 devices")
	}
	if len(result.Items) != 1 {
		t.Errorf("expected 1 item, got %d", len(result.Items))
	}
	// First item should be "alpha" (name ASC sort)
	if result.Items[0].Name != "alpha" {
		t.Errorf("expected first item to be 'alpha' (name ASC), got %q", result.Items[0].Name)
	}
}

func TestService_ListSecondarySort(t *testing.T) {
	// Two devices with same name — secondary sort by id ASC must be stable.
	repo := mem.NewDeviceRepository()
	seedDevice(t, repo, "dev-z", "sensor", "offline")
	seedDevice(t, repo, "dev-a", "sensor", "online")

	svc, err := NewService(repo, newTestCodec(t), slog.Default(), query.RunModeDemo)
	if err != nil {
		t.Fatal(err)
	}

	result, err := svc.List(context.Background(), query.PageRequest{Limit: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(result.Items))
	}
	// id ASC: "dev-a" before "dev-z"
	if result.Items[0].ID != "dev-a" {
		t.Errorf("expected first item id='dev-a' (id ASC), got %q", result.Items[0].ID)
	}
	if result.Items[1].ID != "dev-z" {
		t.Errorf("expected second item id='dev-z', got %q", result.Items[1].ID)
	}
}
