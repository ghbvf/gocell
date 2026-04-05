// Package domain defines the core domain model for the device-cell example.
package domain

import (
	"context"
	"time"
)

// Device represents an IoT device aggregate.
type Device struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Status   string    `json:"status"` // online, offline
	LastSeen time.Time `json:"lastSeen"`
}

// Command represents a command dispatched to a device.
// In the L4 DeviceLatent model, commands are enqueued by the server and
// polled by the device on its own schedule. The device acknowledges
// execution via the ack endpoint.
type Command struct {
	ID        string     `json:"id"`
	DeviceID  string     `json:"deviceId"`
	Payload   string     `json:"payload"`
	Status    string     `json:"status"` // pending, acked
	CreatedAt time.Time  `json:"createdAt"`
	AckedAt   *time.Time `json:"ackedAt,omitempty"`
}

// DeviceRepository abstracts device persistence.
type DeviceRepository interface {
	Create(ctx context.Context, device *Device) error
	GetByID(ctx context.Context, id string) (*Device, error)
}

// CommandRepository abstracts command persistence.
type CommandRepository interface {
	Create(ctx context.Context, cmd *Command) error
	ListPending(ctx context.Context, deviceID string) ([]*Command, error)
	Ack(ctx context.Context, deviceID, cmdID string) error
}
