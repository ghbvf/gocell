// Package devicecommand implements the public device-command slice: enqueue /
// dequeue / report / ack / extend-lease under /api/v1/devices (device/operator-
// gated). The internal control-plane list (/internal/v1/devicecommands) lives
// in the sibling devicecommandinternal slice; both share
// examples/iotdevice/cells/devicecell/internal/devicecmd.
// Keeping public and internal HTTP surfaces in separate slices is enforced by
// governance rule SLICE-HTTP-VISIBILITY-SEGREGATION-01 (FMT-33).
package devicecommand

import "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/devicecmd"

// Service is the slice service type; the command logic is shared with the
// internal slice via examples/iotdevice/cells/devicecell/internal/devicecmd.
type Service = devicecmd.Service
