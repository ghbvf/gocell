package main

import (
	"context"
	"fmt"

	accesscore "github.com/ghbvf/gocell/cells/accesscore"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/bootstrap"
)

// AccessCoreModule wires accesscore: JWT issuer/verifier + EventBus +
// initial-admin bootstrap worker + cursor codec.
// It reads accesscore-namespaced environment variables directly.
//
// ref: uber-go/fx fx.Module("accesscore", ...) — self-contained module.
// backlog: S29 CORE-BUNDLE-APP-BUILDER-01
type AccessCoreModule struct {
	// InitialAdminOpts are additional options for the initial-admin bootstrap
	// path. Production leaves this nil so default bcrypt cost=12 is used.
	// Tests inject a low-cost hasher to avoid blocking CI.
	InitialAdminOpts []accesscore.InitialAdminOption
}

// ID returns the stable identifier used in error messages.
func (AccessCoreModule) ID() string { return "accesscore" }

// Provide resolves all accesscore-specific dependencies and returns the
// constructed cell and the lazy admin bootstrap worker option.
//
// Reads GOCELL_ACCESSCORE_CURSOR_KEY and GOCELL_ACCESSCORE_CURSOR_PREVIOUS_KEY
// from the environment.
func (m AccessCoreModule) Provide(_ context.Context, shared *SharedDeps) (cell.Cell, []bootstrap.Option, error) {
	// Cursor codec for accesscore: read env via LoadCursorKeys then build.
	accessPrimary, accessPrevious := LoadCursorKeys("ACCESSCORE")
	cursorCodec, err := buildCursorCodec(shared.Topology.AdapterMode,
		"GOCELL_ACCESSCORE_CURSOR_KEY", "GOCELL_ACCESSCORE_CURSOR_PREVIOUS_KEY",
		accessPrimary, accessPrevious,
		"corebundle-access-cursor-key32!!", "access")
	if err != nil {
		return nil, nil, fmt.Errorf("accesscore cursor codec: %w", err)
	}

	accessOpts, adminWorkerOpt := adminBootstrapWorkerOpts([]accesscore.Option{
		accesscore.WithInMemoryDefaults(),
		accesscore.WithPublisher(shared.EventBus),
		accesscore.WithJWTIssuer(shared.JWTDeps.issuer),
		accesscore.WithJWTVerifier(shared.JWTDeps.verifier),
		accesscore.WithCursorCodec(cursorCodec),
	}, m.InitialAdminOpts...)
	c := accesscore.NewAccessCore(accessOpts...)

	var opts []bootstrap.Option
	if adminWorkerOpt != nil {
		opts = append(opts, adminWorkerOpt)
	}
	return c, opts, nil
}

var _ CellModule = AccessCoreModule{}
