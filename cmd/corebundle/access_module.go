package main

import (
	"context"
	"fmt"
	"os"
	"time"

	accesscore "github.com/ghbvf/gocell/cells/accesscore"
	"github.com/ghbvf/gocell/cells/accesscore/initialadmin"
	"github.com/ghbvf/gocell/kernel/cell"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/runtime/bootstrap"
)

// AdminProvisionModeEnv selects how the first admin is provisioned.
//
//	"interactive" (default) — no admin created at startup; operator must POST
//	                          to /api/v1/access/setup/admin to create one.
//	                          setup GET returns {"hasAdmin":false} until done.
//	"bootstrap"             — initialadmin Lifecycle runs at startup, generates
//	                          a random password, writes it to the credential
//	                          file for out-of-band retrieval. setup POST is
//	                          effectively 410 for the lifetime of the deployment.
//
// Two modes are mutually exclusive by construction: "bootstrap" enables the
// Lifecycle that creates the admin; "interactive" leaves the provisioning job
// to the HTTP endpoint. This removes the "double-owner" ambiguity where both
// were wired simultaneously and whichever raced first won.
const AdminProvisionModeEnv = "GOCELL_ACCESSCORE_ADMIN_PROVISION_MODE"

// AccessCoreModule wires accesscore: JWT issuer/verifier + EventBus + cursor
// codec, and conditionally the initial-admin bootstrap Lifecycle when the
// GOCELL_ACCESSCORE_ADMIN_PROVISION_MODE environment variable selects it.
//
// ref: uber-go/fx fx.Module("accesscore", ...) — self-contained module.
// backlog: S29 CORE-BUNDLE-APP-BUILDER-01
type AccessCoreModule struct {
	// InitialAdminOpts are additional options passed to the initial-admin
	// bootstrap Lifecycle when GOCELL_ACCESSCORE_ADMIN_PROVISION_MODE=bootstrap.
	// Production leaves this nil so default bcrypt cost=12 is used; tests
	// inject a low-cost hasher to avoid blocking CI.
	InitialAdminOpts []initialadmin.LifecycleOption

	// ForceBootstrap, when true, enables the initial-admin Lifecycle regardless
	// of the environment variable. Used by integration tests that want to
	// exercise the bootstrap path without setting the env var in the test
	// process. Production code must not set this; go through the env var.
	ForceBootstrap bool
}

// ID returns the stable identifier used in error messages.
func (AccessCoreModule) ID() string { return "accesscore" }

// Provide resolves all accesscore-specific dependencies and returns the
// constructed cell, the lazy admin bootstrap worker option, and nil
// provisional resources (accesscore is in-memory only).
//
// Reads GOCELL_ACCESSCORE_CURSOR_KEY and GOCELL_ACCESSCORE_CURSOR_PREVIOUS_KEY
// from the environment.
func (m AccessCoreModule) Provide(_ context.Context, shared *SharedDeps) (cell.Cell, []bootstrap.Option, []kernellifecycle.ManagedResource, error) {
	// Cursor codec for accesscore: read env via LoadCursorKeys then build.
	accessPrimary, accessPrevious := LoadCursorKeys("ACCESSCORE")
	cursorCodec, err := buildCursorCodec(cursorCodecConfig{
		AdapterMode: shared.Topology.AdapterMode,
		EnvName:     "GOCELL_ACCESSCORE_CURSOR_KEY",
		PrevEnvName: "GOCELL_ACCESSCORE_CURSOR_PREVIOUS_KEY",
		Primary:     accessPrimary,
		Previous:    accessPrevious,
		DevDefault:  "corebundle-access-cursor-key32!!",
		Label:       "access",
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("accesscore cursor codec: %w", err)
	}

	accessOpts := []accesscore.Option{
		accesscore.WithInMemoryDefaults(),
		// Demo-mode wiring: publisher only, no outboxWriter — cell.ResolveEmitter
		// picks DirectEmitter(FailOpen) and keeps L2 slices running non-durably.
		accesscore.WithOutboxDeps(shared.EventBus, nil),
		accesscore.WithJWTIssuer(shared.JWTDeps.issuer),
		accesscore.WithJWTVerifier(shared.JWTDeps.verifier),
		accesscore.WithCursorCodec(cursorCodec),
		accesscore.WithRefreshMetricsProvider(shared.PromStack.metricProvider),
		accesscore.WithRefreshGC(time.Hour, 24*time.Hour),
	}
	if m.ForceBootstrap || os.Getenv(AdminProvisionModeEnv) == "bootstrap" {
		accessOpts = append(accessOpts, accesscore.WithInitialAdminBootstrap(m.InitialAdminOpts...))
	}
	c := accesscore.NewAccessCore(accessOpts...)
	// Bootstrap phase3b auto-discovers c.LifecycleHooks() — no WithWorkers needed.
	return c, nil, nil, nil
}

var _ CellModule = AccessCoreModule{}
