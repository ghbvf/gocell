package status

import "context"

// Repository is the port for cert-status persistence. The slice owns this
// interface; the composition root wires the in-memory implementation (mem.New)
// for the demo topology. A postgres-backed implementation lands in PR-15.
//
// ActiveByDeviceID returns the CertRecord with the highest Epoch for deviceID.
// Returns (CertRecord{}, false, nil) when no record exists for the given device.
type Repository interface {
	ActiveByDeviceID(ctx context.Context, deviceID string) (CertRecord, bool, error)
}
