// Package noncanonical_applier_interface_red is a RED fixture for
// CREDENTIAL-INVALIDATE-APPLIER-INTERFACE-CANONICAL-01: it declares a
// LocalApplier interface whose Apply method has the same signature as
// credentialinvalidate.Applier.Apply but lives outside the
// credentialinvalidate package. The scanner must detect this regression —
// allowing such a re-declaration would re-create the pre-#1196
// sessionrefresh "interface-routed Soft channel" (info.Selections would
// resolve s.inv.Apply to the local interface, hiding the callsite from
// CREDENTIAL-INVALIDATE-UPSTREAM-CALLER-01).
package noncanonical_applier_interface_red

import (
	"context"

	"github.com/ghbvf/gocell/runtime/auth/session"
)

// LocalApplier is the regression pattern: identical signature to
// credentialinvalidate.Applier.Apply, declared in a non-canonical package.
// The archtest must flag it.
type LocalApplier interface {
	Apply(ctx context.Context, subjectID string, event session.CredentialEvent) error
}
