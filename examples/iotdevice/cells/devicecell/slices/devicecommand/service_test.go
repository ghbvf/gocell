package devicecommand

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/mem"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/command"
	"github.com/ghbvf/gocell/kernel/command/commandtest"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
)

func testCodec() *query.CursorCodec {
	codec, _ := query.NewCursorCodec(bytes.Repeat([]byte("k"), 32))
	return codec
}

// enqueueTestCmd enqueues a single test command entry with standard fixture
// values (cmdID="cmd-1", deviceID="dev-1"). Use this only when the test does
// not care about the specific identifiers — concurrent or pagination tests
// that need distinct cmdIDs/deviceIDs should construct command.NewEntry
// inline so each entry stays grep-able from the assertion.
func enqueueTestCmd(ctx context.Context, q *commandtest.InMemQueue) error {
	entry := command.NewEntry("cmd-1", "dev-1", "reboot", []byte("x"), command.Timeouts{}, time.Now())
	return q.Enqueue(ctx, entry, command.EnqueueOptions{})
}

func TestNewService_NilCodec_ReturnsError(t *testing.T) {
	devRepo := mem.NewDeviceRepository()
	q := commandtest.NewInMemQueue()
	svc, err := NewService(q, devRepo, nil, slog.Default(), query.RunModeProd, WithClock(clock.Real()))
	require.Error(t, err)
	assert.Nil(t, svc)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCellMissingCodec, ecErr.Code)
}

func newTestService() (*Service, *mem.DeviceRepository, *commandtest.InMemQueue) {
	devRepo := mem.NewDeviceRepository()
	q := commandtest.NewInMemQueue()
	svc, err := NewService(q, devRepo, testCodec(), slog.Default(), query.RunModeProd, WithClock(clock.Real()))
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

			entry, err := svc.enqueueInternal(context.Background(), tc.deviceID, tc.commandType, tc.payload)
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

	entry, err := svc.enqueueInternal(context.Background(), "dev-1", "", "reboot")
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
	_, err := svc.enqueueInternal(context.Background(), "dev-nonexistent", "", "reboot")
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrAuthForbidden, ecErr.Code,
		"authz must be checked before device lookup — must return Forbidden, not NotFound")
}

func TestService_Dequeue(t *testing.T) {
	svc, devRepo, q := newTestService()
	ctx := context.Background()
	now := time.Now()
	seedDevice(devRepo, "dev-1", "sensor-a")
	seedDevice(devRepo, "dev-2", "sensor-b")

	// Enqueue 2 commands for dev-1 and 1 for dev-2.
	opts := command.EnqueueOptions{}
	require.NoError(t, q.Enqueue(ctx,
		command.NewEntry("c1", "dev-1", "reboot", []byte("a"), command.Timeouts{}, now), opts))
	require.NoError(t, q.Enqueue(ctx,
		command.NewEntry("c2", "dev-1", "reboot", []byte("b"), command.Timeouts{}, now.Add(time.Second)), opts))
	require.NoError(t, q.Enqueue(ctx,
		command.NewEntry("c3", "dev-2", "reboot", []byte("c"), command.Timeouts{}, now), opts))

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
			entries, err := svc.dequeueInternal(ctx, tc.deviceID, 10, command.DefaultLeaseDuration)
			if tc.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Len(t, entries, tc.wantLen)
				for _, entry := range entries {
					assert.Equal(t, command.StatusSent, entry.Status)
					assert.NotNil(t, entry.SentAt)
				}
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
			name: "ack sent command succeeds",
			setup: func(dr *mem.DeviceRepository, q *commandtest.InMemQueue) {
				seedDevice(dr, "dev-1", "sensor-a")
				ctx := context.Background()
				_ = enqueueTestCmd(ctx, q)
				_, _ = q.Dequeue(ctx, "dev-1", 1, command.DefaultLeaseDuration)
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
				ctx := context.Background()
				_ = q.Enqueue(ctx, command.NewEntry("cmd-2", "dev-1", "reboot", []byte("x"), command.Timeouts{}, time.Now()), command.EnqueueOptions{})
				_, _ = q.Dequeue(ctx, "dev-1", 1, command.DefaultLeaseDuration)
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

			err := svc.ackInternal(context.Background(), tc.deviceID, tc.cmdID, command.AckSuccess)
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
	require.NoError(t, enqueueTestCmd(ctx, q))
	_, err := q.Dequeue(ctx, "dev-1", 1, command.DefaultLeaseDuration)
	require.NoError(t, err)

	// Ack once
	require.NoError(t, svc.ackInternal(ctx, "dev-1", "cmd-1", command.AckSuccess))

	// Ack again — should be idempotent (no error)
	require.NoError(t, svc.ackInternal(ctx, "dev-1", "cmd-1", command.AckSuccess))
}

func TestService_Ack_ConcurrentSameReason_Idempotent(t *testing.T) {
	svc, devRepo, q := newTestService()
	ctx := context.Background()
	seedDevice(devRepo, "dev-1", "sensor-a")
	require.NoError(t, enqueueTestCmd(ctx, q))
	_, err := q.Dequeue(ctx, "dev-1", 1, command.DefaultLeaseDuration)
	require.NoError(t, err)

	const workers = 16
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for range workers {
		wg.Go(func() {
			errs <- svc.ackInternal(ctx, "dev-1", "cmd-1", command.AckSuccess)
		})
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}
	got, err := q.GetCommand(ctx, "cmd-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, command.StatusSucceeded, got.Status)
}

func TestService_Ack_ConcurrentDifferentReason_RejectsLoser(t *testing.T) {
	svc, devRepo, q := newTestService()
	ctx := context.Background()
	seedDevice(devRepo, "dev-1", "sensor-a")
	require.NoError(t, enqueueTestCmd(ctx, q))
	_, err := q.Dequeue(ctx, "dev-1", 1, command.DefaultLeaseDuration)
	require.NoError(t, err)

	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, reason := range []command.AckReason{command.AckSuccess, command.AckFailed} {
		wg.Add(1)
		go func(reason command.AckReason) {
			defer wg.Done()
			errs <- svc.ackInternal(ctx, "dev-1", "cmd-1", reason)
		}(reason)
	}
	wg.Wait()
	close(errs)

	var successCount, errorCount int
	for err := range errs {
		if err == nil {
			successCount++
			continue
		}
		errorCount++
	}
	assert.Equal(t, 1, successCount)
	assert.Equal(t, 1, errorCount)
}

func TestService_Ack_LifecycleSentToSucceeded(t *testing.T) {
	svc, devRepo, q := newTestService()
	ctx := context.Background()
	seedDevice(devRepo, "dev-1", "sensor-a")
	require.NoError(t, enqueueTestCmd(ctx, q))
	_, err := q.Dequeue(ctx, "dev-1", 1, command.DefaultLeaseDuration)
	require.NoError(t, err)

	// Ack from Sent → Succeeded without synthesizing Delivered.
	require.NoError(t, svc.ackInternal(ctx, "dev-1", "cmd-1", command.AckSuccess))

	// Entry should now be Succeeded (terminal).
	got, err := q.GetCommand(ctx, "cmd-1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, command.StatusSucceeded, got.Status)
	assert.NotNil(t, got.SentAt)
	assert.Nil(t, got.DeliveredAt)
	assert.NotNil(t, got.CompletedAt)
}

func TestService_ExtendLease_RejectsTooLargeExtension(t *testing.T) {
	svc, devRepo, q := newTestService()
	ctx := context.Background()
	seedDevice(devRepo, "dev-1", "sensor-a")
	require.NoError(t, enqueueTestCmd(ctx, q))
	_, err := q.Dequeue(ctx, "dev-1", 1, command.DefaultLeaseDuration)
	require.NoError(t, err)

	err = svc.extendLeaseInternal(ctx, "dev-1", "cmd-1", time.Hour+time.Second)
	require.Error(t, err)
}

func TestService_ScanActive_CursorDeviceMismatch(t *testing.T) {
	svc, devRepo, q := newTestService()
	ctx := context.Background()
	now := time.Now()
	seedDevice(devRepo, "dev-A", "sensor-a")
	seedDevice(devRepo, "dev-B", "sensor-b")

	// Enqueue enough commands for dev-A so a cursor is generated.
	for i := range 5 {
		ts := now.Add(time.Duration(i) * time.Second)
		_ = q.Enqueue(ctx, command.NewEntry(
			"c"+string(rune('0'+i)), "dev-A", "reboot", []byte("x"),
			command.Timeouts{}, ts,
		), command.EnqueueOptions{})
	}

	// Get first page for dev-A.
	page1, err := svc.ScanActive(ctx, command.ScanFilter{DeviceID: "dev-A"}, query.PageParams{Limit: 3})
	require.NoError(t, err)
	require.True(t, page1.HasMore)
	require.NotEmpty(t, page1.NextCursor)

	// Replay the cursor against dev-B — must fail with context mismatch.
	_, err = svc.ScanActive(ctx, command.ScanFilter{DeviceID: "dev-B"}, query.PageParams{Limit: 3, Cursor: page1.NextCursor})
	require.Error(t, err)
	var ecErr *errcode.Error
	require.ErrorAs(t, err, &ecErr)
	assert.Equal(t, errcode.ErrCursorInvalid, ecErr.Code)
	reasonAttr, ok := ecErr.FindAttr("reason")
	require.True(t, ok)
	assert.Equal(t, "query context mismatch", reasonAttr.Value.String())
}

func TestService_Enqueue_ThenDequeue_Report_ThenAck(t *testing.T) {
	svc, devRepo, q := newTestService()
	ctx := context.Background()
	seedDevice(devRepo, "dev-1", "sensor-a")

	// Enqueue
	entry, err := svc.enqueueInternal(ctx, "dev-1", "", "upgrade-fw")
	require.NoError(t, err)

	// Dequeue claims the command and marks it Sent.
	entries, err := svc.dequeueInternal(ctx, "dev-1", 10, command.DefaultLeaseDuration)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, entry.ID, entries[0].ID)
	assert.Equal(t, command.StatusSent, entries[0].Status)

	require.NoError(t, svc.reportInternal(ctx, "dev-1", entry.ID))
	require.NoError(t, svc.ackInternal(ctx, "dev-1", entry.ID, command.AckSuccess))

	// Scan active should be empty after ack (command is now terminal).
	result, err := svc.ScanActive(ctx, command.ScanFilter{DeviceID: "dev-1"}, query.PageParams{})
	require.NoError(t, err)
	assert.Empty(t, result.Items)

	// Verify terminal state in queue.
	got, err := q.GetCommand(ctx, entry.ID)
	require.NoError(t, err)
	assert.True(t, got.Status.IsTerminal())
}
