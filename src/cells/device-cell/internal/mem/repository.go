// Package mem provides in-memory implementations of the device domain repositories.
package mem

import (
	"cmp"
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ghbvf/gocell/cells/device-cell/internal/domain"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
)

// Compile-time interface checks.
var (
	_ domain.DeviceRepository  = (*DeviceRepository)(nil)
	_ domain.CommandRepository = (*CommandRepository)(nil)
)

// DeviceRepository is a thread-safe in-memory device store.
type DeviceRepository struct {
	mu      sync.RWMutex
	devices map[string]*domain.Device
}

// NewDeviceRepository creates an empty in-memory DeviceRepository.
func NewDeviceRepository() *DeviceRepository {
	return &DeviceRepository{devices: make(map[string]*domain.Device)}
}

// Create stores a new device. Returns an error if the ID already exists.
func (r *DeviceRepository) Create(_ context.Context, device *domain.Device) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.devices[device.ID]; exists {
		return errcode.New(errcode.ErrValidationFailed,
			fmt.Sprintf("device %q already exists", device.ID))
	}
	stored := *device
	r.devices[device.ID] = &stored
	return nil
}

// GetByID retrieves a device by ID.
func (r *DeviceRepository) GetByID(_ context.Context, id string) (*domain.Device, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	d, ok := r.devices[id]
	if !ok {
		return nil, errcode.New(errcode.ErrDeviceNotFound,
			fmt.Sprintf("device %q not found", id))
	}
	out := *d
	return &out, nil
}

// CommandRepository is a thread-safe in-memory command store.
type CommandRepository struct {
	mu       sync.RWMutex
	commands map[string]*domain.Command // keyed by command ID
}

// NewCommandRepository creates an empty in-memory CommandRepository.
func NewCommandRepository() *CommandRepository {
	return &CommandRepository{commands: make(map[string]*domain.Command)}
}

// Create stores a new command.
func (r *CommandRepository) Create(_ context.Context, cmd *domain.Command) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.commands[cmd.ID]; exists {
		return errcode.New(errcode.ErrValidationFailed,
			fmt.Sprintf("command %q already exists", cmd.ID))
	}
	stored := *cmd
	r.commands[cmd.ID] = &stored
	return nil
}

// ListPending returns pending commands for the given device, sorted and
// paginated according to params. It returns up to FetchLimit() rows for
// N+1 hasMore detection.
func (r *CommandRepository) ListPending(_ context.Context, deviceID string, params query.ListParams) ([]*domain.Command, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Filter by deviceID and pending status.
	var filtered []*domain.Command
	for _, cmd := range r.commands {
		if cmd.DeviceID == deviceID && cmd.Status == "pending" {
			cp := *cmd
			filtered = append(filtered, &cp)
		}
	}

	query.Sort(filtered, params.Sort, compareCommandField)
	return query.ApplyCursor(filtered, params, commandFieldValue), nil
}

// compareCommandField compares a single field of two commands.
func compareCommandField(a, b *domain.Command, field string) int {
	switch field {
	case "created_at":
		return a.CreatedAt.Compare(b.CreatedAt)
	case "id":
		return cmp.Compare(a.ID, b.ID)
	case "device_id":
		return cmp.Compare(a.DeviceID, b.DeviceID)
	case "payload":
		return cmp.Compare(a.Payload, b.Payload)
	case "status":
		return cmp.Compare(a.Status, b.Status)
	default:
		return 0
	}
}

// commandFieldValue extracts a cursor-comparable value from a command.
func commandFieldValue(cmd *domain.Command, field string) any {
	switch field {
	case "created_at":
		return cmd.CreatedAt
	case "id":
		return cmd.ID
	case "device_id":
		return cmd.DeviceID
	case "payload":
		return cmd.Payload
	case "status":
		return cmd.Status
	default:
		return ""
	}
}

// Ack marks a command as acknowledged.
func (r *CommandRepository) Ack(_ context.Context, deviceID, cmdID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	cmd, ok := r.commands[cmdID]
	if !ok {
		return errcode.New(errcode.ErrCommandNotFound,
			fmt.Sprintf("command %q not found", cmdID))
	}
	if cmd.DeviceID != deviceID {
		return errcode.New(errcode.ErrValidationFailed,
			fmt.Sprintf("command %q does not belong to device %q", cmdID, deviceID))
	}
	if cmd.Status == "acked" {
		return nil // idempotent
	}
	now := time.Now()
	cmd.Status = "acked"
	cmd.AckedAt = &now
	return nil
}
