//go:build archtest_fixture

// Package authorizeascallerfixture is a RED fixture for
// AUTHZ-AUTHORIZE-AS-CALLER-FUNNEL-01. It calls auth.SubjectAuthorizer.AuthorizeAs
// (the explicit-subject PDP seam) from a package that is NOT on the caller
// allowlist (only the certsigning/pdpauthz adapter and the bootstrap lazy
// delegation may), so the use-based detector must flag the reference. It is never
// imported by production code; it exists only so the archtest reverse self-check
// can prove the detector fires.
//
// Gated behind the archtest_fixture build tag so it is invisible to the
// Production() scan (which would otherwise flag it as an unsanctioned caller) but
// loaded by the Fixture() façade in the reverse self-check.
package authorizeascallerfixture

import (
	"context"

	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/runtime/auth"
)

// ForgeAuthorizeAs reaches the explicit-subject seam from an unsanctioned caller
// (RED): a business package could forge an arbitrary device subject and bypass
// the ambient-principal Authorize gate. This is the INTERFACE-typed call form.
func ForgeAuthorizeAs(ctx context.Context, pdp auth.SubjectAuthorizer) bool {
	desc, err := auth.NewDeviceSubjectDescriptor(
		"11111111-1111-1111-1111-111111111111",
		"forged-device",
	)
	if err != nil {
		return false
	}
	dec, err := pdp.AuthorizeAs(ctx, desc, "forged-device", "device:enroll")
	if err != nil {
		return false
	}
	return dec.IsAllow()
}

// concretePDP is a NON-interface (concrete) SubjectAuthorizer-shaped type — its
// AuthorizeAs has the seam signature (second param auth.SubjectDescriptor). It
// mirrors accesscore authorizationdecide.Service: a concrete impl the composition
// root can hold directly.
type concretePDP struct{}

func (concretePDP) AuthorizeAs(_ context.Context, _ auth.SubjectDescriptor, _, _ string) (authz.Decision, error) {
	return authz.Decision{}, nil
}

// ForgeConcreteAuthorizeAs reaches the seam via a DIRECT CONCRETE-RECEIVER call
// (RED) — the bypass form that anchoring on the interface package missed (#1904
// review F4). The signature-shape detector must flag this exactly like the
// interface call above.
func ForgeConcreteAuthorizeAs(ctx context.Context) bool {
	var pdp concretePDP
	desc, err := auth.NewDeviceSubjectDescriptor(
		"11111111-1111-1111-1111-111111111111",
		"forged-device",
	)
	if err != nil {
		return false
	}
	dec, err := pdp.AuthorizeAs(ctx, desc, "forged-device", "device:enroll")
	if err != nil {
		return false
	}
	return dec.IsAllow()
}
