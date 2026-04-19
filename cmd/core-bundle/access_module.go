package main

import (
	"context"
	"fmt"

	accesscore "github.com/ghbvf/gocell/cells/access-core"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/bootstrap"
)

// AccessCoreModule wires access-core: JWT issuer/verifier + EventBus +
// initial-admin bootstrap worker.
//
// ref: uber-go/fx fx.Module("access-core", ...) — self-contained module.
// backlog: S29 CORE-BUNDLE-APP-BUILDER-01
type AccessCoreModule struct {
	// InitialAdminOpts are additional options for the initial-admin bootstrap
	// path. Production leaves this nil so default bcrypt cost=12 is used.
	// Tests inject a low-cost hasher to avoid blocking CI.
	InitialAdminOpts []accesscore.InitialAdminOption
}

// ID returns the stable identifier used in error messages.
func (AccessCoreModule) ID() string { return "access-core" }

// Provide resolves all access-core-specific dependencies and returns the
// constructed cell and the lazy admin bootstrap worker option.
func (m AccessCoreModule) Provide(_ context.Context, sharedProv bootstrap.SharedDepsProvider) (cell.Cell, []bootstrap.Option, error) {
	s, ok := sharedProv.(*SharedDeps)
	if !ok {
		return nil, nil, fmt.Errorf("access-core: expected *SharedDeps, got %T", sharedProv)
	}

	accessOpts, adminWorkerOpt := adminBootstrapWorkerOpts([]accesscore.Option{
		accesscore.WithInMemoryDefaults(),
		accesscore.WithPublisher(s.EventBus),
		accesscore.WithJWTIssuer(s.JWTDeps.issuer),
		accesscore.WithJWTVerifier(s.JWTDeps.verifier),
	}, m.InitialAdminOpts...)
	c := accesscore.NewAccessCore(accessOpts...)

	var opts []bootstrap.Option
	if adminWorkerOpt != nil {
		opts = append(opts, adminWorkerOpt)
	}
	return c, opts, nil
}

var _ bootstrap.CellModule = AccessCoreModule{}
