package devicecommand

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/mem"
	"github.com/ghbvf/gocell/kernel/command"
	"github.com/ghbvf/gocell/kernel/command/commandtest"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testCodec() *query.CursorCodec {
	codec, _ := query.NewCursorCodec(bytes.Repeat([]byte("k"), 32))
	return codec
}

func TestNewService_NilCodec_ReturnsError(t *testing.T) {
	devRepo := mem.NewDeviceRepository()
	q := commandtest.NewInMemQueue()
	svc, err := NewService(q, devRepo, nil, slog.Default(), query.RunModeProd)
	require.Error(t, err)
	assert.Nil(t, svc)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCellMissingCodec, ecErr.Code)
}

func newTestService() (*Service, *mem.DeviceRepository, *commandtest.InMemQueue) {
	devRepo := mem.NewDeviceRepository()
	q := commandtest.NewInMemQueue()
	svc, err := NewService(q, devRepo, testCodec(), slog.Default(), query.RunModeProd)
	if err != nil {
		panic(err)
	}
	return svc, devRepo, q
}

func seedDevice(repo *mem.DeviceRepository, id, name string) {
	_ = repo.Create(context.Background(), &domain.Device{
		ID: id, Name: name, Status: "online",
	})
}

func TestService_Enqueue(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(*mem.DeviceRepository)
		deviceID    string
		commandType string
		payload     string
		wantErr     bool
		checkEntry  func(t *testing.T, e command.Entry)
	}{
		{
			name:        "valid enqueue uses default commandType",
			setup:       func(r *mem.DeviceRepository) { seedDevice(r, "dev-1", "sensor-a") },
			deviceID:    "dev-1",
			commandType: "",
			payload:     "reboot",
			wantErr:     false,
			checkEntry: func(t *testing.T, e command.Entry) {
				assert.NotEmpty(t, e.ID)
				assert.Equal(t, "dev-1", e.DeviceID)
				assert.Equal(t, "default", e.CommandType)
				assert.Equal(t, []byte("reboot"), e.Payload)
				assert.Equal(t, command.StatusPending, e.Status)
				assert.False(t, e.CreatedAt.IsZero())
			},
		},
		{
			name:        "valid enqueue with explicit commandType",
			setup:       func(r *mem.DeviceRepository) { seedDevice(r, "dev-1", "sensor-a") },
			deviceID:    "dev-1",
			commandType: "firmware-update",
			payload:     "v2.0",
			wantErr:     false,
			checkEntry: func(t *testing.T, e command.Entry) {
				assert.Equal(t, "firmware-update", e.CommandType)
				assert.Equal(t, command.StatusPending, e.Status)
			},
		},
		{
			name:        "non-existent device returns error",
			setup:       func(_ *mem.DeviceRepository) {},
			deviceID:    "dev-missing",
			commandType: "",
			payload:     "reboot",
			wantErr:     true,
		},
		{
			name:        "empty payload returns validation error",
			setup:       func(r *mem.DeviceRepository) { seedDevice(r, "dev-2", "sensor-b") },
			deviceID:    "dev-2",
			commandType: "",
			payload:     "",
			wantErr:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, devRepo, _ := newTestService()
			tc.setup(devRepo)

			entry, err := svc.Enqueue(context.Background(), tc.deviceID, tc.commandType, tc.payload)
			if tc.wantErr {
				assert.Error(t, err)
				assert.Zero(t, entry)
			} else {
				require.NoError(t, err)
				if tc.checkEntry != nil {
					tc.checkEntry(t, entry)
				}
			}
		})
	}
}

func TestService_Enqueue_AuthzHook(t *testing.T) {
	svc, devRepo, _ := newTestService()
	seedDevice(devRepo, "dev-1", "sensor-a")

	// Authz func that always rejects.
	rejectAll := func(_ context.Context) error {
		return errors.New("permission denied")
	}
	svc.authz = rejectAll

	entry, err := svc.Enqueue(context.Background(), "dev-1", "", "reboot")
	assert.Error(t, err)
	assert.Zero(t, entry)
	assert.Contains(t, err.Error(), "permission denied")
}

func TestService_Enqueue_AuthzCheckedBeforeDeviceLookup(t *testing.T) {
	// Authz must fire before device lookup to prevent 404 vs 403 timing probing.
	// With a non-existent device AND a rejecting authz, the result must be Forbidden
	// (authz error), not NotFound (device lookup error).
	svc, _, _ := newTestService()

	rejectAll := func(_ context.Context) error {
		return errors.New("permission denied")
	}
	svc.authz = rejectAll

	// deviceID does not exist — but authz fires first and returns Forbidden.
	_, err := svc.Enqueue(context.Background(), "dev-nonexistent", "", "reboot")
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrAuthForbidden, ecErr.Code,
		"authz must be checked before device lookup — must return Forbidden, not NotFound")
}

func TestService_ListPending(t *testing.T) {
	svc, devRepo, q := newTestService()
	ctx := context.Background()
	now := time.Now()
	seedDevice(devRepo, "dev-1", "sensor-a")
	seedDevice(devRepo, "dev-2", "sensor-b")

	// Enqueue 2 commands for dev-1 and 1 for dev-2.
	require.NoError(t, q.Enqueue(ctx, command.NewEntry("c1", "dev-1", "reboot", []byte("a"), command.Timeouts{}, now), command.EnqueueOptions{}))
	require.NoError(t, q.Enqueue(ctx, command.NewEntry("c2", "dev-1", "reboot", []byte("b"), command.Timeouts{}, now.Add(time.Second)), command.EnqueueOptions{}))
	require.NoError(t, q.Enqueue(ctx, command.NewEntry("c3", "dev-2", "reboot", []byte("c"), command.Timeouts{}, now), command.EnqueueOptions{}))

	tests := []struct {
		name     string
		deviceID string
		wantLen  int
		wantErr  bool
	}{
		{name: "dev-1 has 2 pending", deviceID: "dev-1", wantLen: 2},
		{name: "dev-2 has 1 pending", deviceID: "dev-2", wantLen: 1},
		{name: "non-existent device returns error", deviceID: "dev-missing", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result, err := svc.ListPending(ctx, tc.deviceID, query.PageParams{})
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Len(t, result.Items, tc.wantLen)
			}
		})
	}
}

func TestService_Ack(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(devRepo *mem.DeviceRepository, q *commandtest.InMemQueue)
		deviceID string
		cmdID    string
		wantErr  bool
	}{
		{
			name: "ack pending command succeeds",
			setup: func(dr *mem.DeviceRepository, q *commandtest.InMemQueue) {
				seedDevice(dr, "dev-1", "sensor-a")
				_ = q.Enqueue(context.Background(), command.NewEntry("cmd-1", "dev-1", "reboot", []byte("x"), command.Timeouts{}, time.Now()), command.EnqueueOptions{})
			},
			deviceID: "dev-1",
			cmdID:    "cmd-1",
			wantErr:  false,
		},
		{
			name: "ack non-existent command returns error",
			setup: func(dr *mem.DeviceRepository, _ *commandtest.InMemQueue) {
				seedDevice(dr, "dev-1", "sensor-a")
			},
			deviceID: "dev-1",
			cmdID:    "cmd-missing",
			wantErr:  true,
		},
		{
			name: "ack with wrong device returns error",
			setup: func(dr *mem.DeviceRepository, q *commandtest.InMemQueue) {
				seedDevice(dr, "dev-1", "sensor-a")
				seedDevice(dr, "dev-2", "sensor-b")
				_ = q.Enqueue(context.Background(), command.NewEntry("cmd-2", "dev-1", "reboot", []byte("x"), command.Timeouts{}, time.Now()), command.EnqueueOptions{})
			},
			deviceID: "dev-2",
			cmdID:    "cmd-2",
			wantErr:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, devRepo, q := newTestService()
			tc.setup(devRepo, q)

			err := svc.Ack(context.Background(), tc.deviceID, tc.cmdID)
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestService_Ack_Idempotent(t *testing.T) {
	svc, devRepo, q := newTestService()
	ctx := context.Background()
	seedDevice(devRepo, "dev-1", "sensor-a")
	require.NoError(t, q.Enqueue(ctx, command.NewEntry("cmd-1", "dev-1", "reboot", []byte("x"), command.Timeouts{}, time.Now()), command.EnqueueOptions{}))

	// Ack once
	require.NoError(t, svc.Ack(ctx, "dev-1", "cmd-1"))

	// Ack again — should be idempotent (no error)
	require.NoError(t, svc.Ack(ctx, "dev-1", "cmd-1"))
}

func TestService_Ack_LifecyclePendingToSucceeded(t *testing.T) {
	svc, devRepo, q := newTestService()
	ctx := context.Background()
	seedDevice(devRepo, "dev-1", "sensor-a")
	require.NoError(t, q.Enqueue(ctx, command.NewEntry("cmd-1", "dev-1", "reboot", []byte("x"), command.Timeouts{}, time.Now()), command.EnqueueOptions{}))

	// Ack from Pending → chains through Sent→Delivered→Succeeded.
	require.NoError(t, svc.Ack(ctx, "dev-1", "cmd-1"))

	// Entry should now be Succeeded (terminal).
	got, err := q.GetCommand(ctx, "cmd-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, command.StatusSucceeded, got.Status)
	assert.NotNil(t, got.CompletedAt)
}

func TestService_ListPending_CursorDeviceMismatch(t *testing.T) {
	svc, devRepo, q := newTestService()
	ctx := context.Background()
	now := time.Now()
	seedDevice(devRepo, "dev-A", "sensor-a")
	seedDevice(devRepo, "dev-B", "sensor-b")

	// Enqueue enough commands for dev-A so a cursor is generated.
	for i := 0; i < 5; i++ {
		ts := now.Add(time.Duration(i) * time.Second)
		_ = q.Enqueue(ctx, command.NewEntry(
			"c"+string(rune('0'+i)), "dev-A", "reboot", []byte("x"),
			command.Timeouts{}, ts,
		), command.EnqueueOptions{})
	}

	// Get first page for dev-A.
	page1, err := svc.ListPending(ctx, "dev-A", query.PageParams{Limit: 3})
	require.NoError(t, err)
	require.True(t, page1.HasMore)
	require.NotEmpty(t, page1.NextCursor)

	// Replay the cursor against dev-B — must fail with context mismatch.
	_, err = svc.ListPending(ctx, "dev-B", query.PageParams{Limit: 3, Cursor: page1.NextCursor})
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCursorInvalid, ecErr.Code)
	assert.Equal(t, "query context mismatch", ecErr.Details["reason"])
}

func TestService_Enqueue_ThenListPending_ThenAck(t *testing.T) {
	svc, devRepo, q := newTestService()
	ctx := context.Background()
	seedDevice(devRepo, "dev-1", "sensor-a")

	// Enqueue
	entry, err := svc.Enqueue(ctx, "dev-1", "", "upgrade-fw")
	require.NoError(t, err)

	// List pending should include the command.
	result, err := svc.ListPending(ctx, "dev-1", query.PageParams{})
	require.NoError(t, err)
	assert.Len(t, result.Items, 1)
	assert.Equal(t, entry.ID, result.Items[0].ID)

	// Ack
	require.NoError(t, svc.Ack(ctx, "dev-1", entry.ID))

	// List pending should be empty after ack (command is now terminal).
	result, err = svc.ListPending(ctx, "dev-1", query.PageParams{})
	require.NoError(t, err)
	assert.Empty(t, result.Items)

	// Verify terminal state in queue.
	got, err := q.GetCommand(ctx, entry.ID)
	require.NoError(t, err)
	assert.True(t, got.Status.IsTerminal())
}
