package status

import (
	"time"

	"github.com/ghbvf/gocell/framework/runtime/certlifecycle"
)

// CertRecord is the in-memory representation of a device certificate entry.
// It mirrors the columns the status endpoint projects via ResponseData, holding
// the fields needed for the L0 read path without importing generated wire types.
//
// State uses the sealed certlifecycle.State type; State.String() maps directly
// to the contract enum values (requested|issued|active|near-expiry|renewing|
// rotated|revoked|expired — confirmed identical by inspection of certlifecycle/state.go).
type CertRecord struct {
	DeviceID    string
	Issuer      string
	Serial      string
	State       certlifecycle.State
	NotBefore   time.Time
	NotAfter    time.Time
	Epoch       int64
	RenewalTime *time.Time
}
