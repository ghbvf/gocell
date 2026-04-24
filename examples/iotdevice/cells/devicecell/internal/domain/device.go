// Package domain defines the core domain model for the devicecell example.
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

// DeviceRepository abstracts device persistence.
type DeviceRepository interface {
	Create(ctx context.Context, device *Device) error
	GetByID(ctx context.Context, id string) (*Device, error)
	// List returns a paginated list of devices sorted by name ASC, id ASC.
	List(ctx context.Context, params query.ListParams) ([]*Device, error)
}
