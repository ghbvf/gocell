// Package domain defines the core domain model for the device-cell example.
package domain

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/pkg/query"
)

// Device represents an IoT device aggregate.
type Device struct {
	ID       string
	Name     string
	Status   string // online, offline
	LastSeen time.Time
}

// Command represents a command dispatched to a device.
// In the L4 DeviceLatent model, commands are enqueued by the server and
// polled by the device on its own schedule. The device acknowledges
// execution via the ack endpoint.
type Command struct {
	ID        string
	DeviceID  string
	Payload   string
	Status    string // pending, acked
	CreatedAt time.Time
	AckedAt   *time.Time
}

// DeviceRepository abstracts device persistence.
type DeviceRepository interface {
	Create(ctx context.Context, device *Device) error
	GetByID(ctx context.Context, id string) (*Device, error)
}

// CommandRepository abstracts command persistence.
type CommandRepository interface {
	Create(ctx context.Context, cmd *Command) error
	ListPending(ctx context.Context, deviceID string, params query.ListParams) ([]*Command, error)
	Ack(ctx context.Context, deviceID, cmdID string) error
}
