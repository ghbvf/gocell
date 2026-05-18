// Package mem exposes devicecell's in-memory repository factory to
// composition roots while keeping the concrete implementation inside the
// cell's internal/mem package.
//
// Mirror of examples/iotdevice/cells/devicecell/postgres for the demo path.
package mem

import (
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	internalmem "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/mem"
)

// NewDeviceRepository constructs an empty in-memory DeviceRepository.
func NewDeviceRepository() domain.DeviceRepository {
	return internalmem.NewDeviceRepository()
}
