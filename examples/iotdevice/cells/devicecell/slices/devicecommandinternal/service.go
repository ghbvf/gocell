// Package devicecommandinternal implements the internal control-plane
// device-command slice: list active commands under /internal/v1/devicecommands,
// mounted on the InternalListener where service-token + caller-cell auth is
// enforced. It shares the command logic with the public devicecommand slice via
// examples/iotdevice/cells/devicecell/internal/devicecmd. Public and internal
// HTTP surfaces are kept in separate slices per governance rule
// SLICE-HTTP-VISIBILITY-SEGREGATION-01 (FMT-33).
package devicecommandinternal

import "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/devicecmd"

// Service is the slice service type; the command logic is shared with the
// public slice via examples/iotdevice/cells/devicecell/internal/devicecmd.
type Service = devicecmd.Service
