package main

import (
	"fmt"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/composition"
)

// validateCorebundleDeps runs the residual cmd-deployment-contract validation
// that does NOT belong to the portable composition contract: it rejects the
// .env.example sample verbose token in adapter mode "real". Every other
// control-plane production check — verbose/metrics tokens, the internal-listener
// guard (InternalHTTPAddr + InternalServiceKeyring), nonce-store kind, and claimer
// kind — moved into composition.SharedDeps.validate (#1410), so external
// composition consumers inherit them fail-closed via NewSharedDeps and no longer
// depend on any cmd-private type.
//
// SampleVerbosePlaceholder is a cmd/corebundle .env.example artifact, not a
// portable contract — an external consumer mints its own placeholders — so this
// single check stays in cmd.
//
// ref: kubernetes/kubernetes cmd/kube-apiserver/app/options/validation.go —
// deployment-specific validation lives with the composition root.
func validateCorebundleDeps(shared *composition.SharedDeps) error {
	if shared == nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "SharedDeps: nil receiver")
	}
	if shared.Topology.RequireProductionControlPlane() && shared.VerboseToken == SampleVerbosePlaceholder {
		return errcode.New(errcode.KindInternal, errcode.ErrControlplaneVerboseTokenSample,
			"GOCELL_READYZ_VERBOSE_TOKEN is set to the .env.example placeholder; "+
				"a production deploy must mint its own high-entropy secret",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("placeholder=%q", SampleVerbosePlaceholder))))
	}
	return nil
}
