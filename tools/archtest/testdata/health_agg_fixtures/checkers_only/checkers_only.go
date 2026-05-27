// Package probesonly declares Probes() but neither Worker() nor Close().
// HEALTH-AGG-01 MUST flag Bad as a violation.
package checkersonly

import "context"

// Probe is a local stand-in for kernel/healthz.Probe. The fixture module is
// isolated (no gocell dependency). The archtest only checks method name
// presence, not the exact return type.
type Probe interface {
	Name() string
	Check(ctx context.Context) error
}

type Bad struct{}

func (*Bad) Probes() []Probe { return nil }
