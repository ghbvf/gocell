package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	accesscore "github.com/ghbvf/gocell/cells/accesscore"
	"github.com/ghbvf/gocell/cells/accesscore/initialadmin"
	"github.com/ghbvf/gocell/kernel/cell"
	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/pkg/errcode"
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

type adminProvisionMode string

const (
	adminProvisionModeInteractive adminProvisionMode = "interactive"
	adminProvisionModeBootstrap   adminProvisionMode = "bootstrap"
)

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
	mode, err := resolveAdminProvisionMode(os.Getenv(AdminProvisionModeEnv), m.ForceBootstrap)
	if err != nil {
		return nil, nil, nil, err
	}
	slog.Info("accesscore: admin provision mode resolved",
		slog.String("mode", string(mode)),
		slog.Bool("force_bootstrap", m.ForceBootstrap))

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
		// Publisher set unconditionally; outboxWriter set conditionally below.
		// cell.ResolveEmitter picks DirectEmitter(FailOpen) when writer is nil
		// (memory mode) and WriterEmitter when both pub+writer are non-nil (durable).
		accesscore.WithOutboxDeps(shared.EventBus, nil),
		accesscore.WithJWTIssuer(shared.JWTDeps.issuer),
		accesscore.WithJWTVerifier(shared.JWTDeps.verifier),
		accesscore.WithCursorCodec(cursorCodec),
		accesscore.WithRefreshMetricsProvider(shared.PromStack.metricProvider),
		accesscore.WithRefreshGC(time.Hour, 24*time.Hour),
	}
	if shared.Topology.StorageBackend == "postgres" {
		if shared.SharedPGPool == nil {
			return nil, nil, nil, fmt.Errorf("AccessCoreModule: postgres mode requires SharedPGPool " +
				"(ConfigCoreModule must run before AccessCoreModule)")
		}
		writer := adapterpg.NewOutboxWriter()
		txMgr := adapterpg.NewTxManager(shared.SharedPGPool)
		// Accumulative WithOutboxDeps: adds writer without replacing the publisher
		// set above. WithTxManager wires the TxRunner for L2 transactional atomicity.
		accessOpts = append(accessOpts,
			accesscore.WithOutboxDeps(nil, writer),
			accesscore.WithTxManager(txMgr),
		)
		// Wire the ConfigClient for the configreceive slice to fetch entry values
		// from configcore's internal GET /internal/v1/config/{key} endpoint after
		// an upsert event (contract: http.config.internal.get.v1).
		// baseURL is constructed from InternalHTTPAddr. If the addr is a port-only
		// string (e.g. ":9090") we resolve to loopback; if host:port, prepend scheme.
		// The HMAC ring from InternalGuard is reused for outbound service-token signing.
		// In dev mode (InternalGuard == nil) the configreceive slice runs in log-only mode.
		internalBaseURL := internalAddrToBaseURL(shared.InternalHTTPAddr)
		if shared.InternalGuard != nil {
			accessOpts = append(accessOpts,
				accesscore.WithConfigClientHTTP(internalBaseURL, shared.InternalGuard.ring),
			)
		}
	}
	if mode == adminProvisionModeBootstrap {
		accessOpts = append(accessOpts, accesscore.WithInitialAdminBootstrap(m.InitialAdminOpts...))
	}
	c := accesscore.NewAccessCore(accessOpts...)
	// Bootstrap phase3b auto-discovers c.LifecycleHooks() — no WithWorkers needed.
	return c, nil, nil, nil
}

var _ CellModule = AccessCoreModule{}

// internalAddrToBaseURL converts a bind address to an HTTP base URL suitable
// for the internal HTTP client. Port-only addresses (e.g. ":9090") are resolved
// to "http://127.0.0.1:9090"; host:port addresses get "http://" prepended.
// As a defensive measure, "0.0.0.0:port" bind addresses are normalised to
// "127.0.0.1:port" so the ConfigClient always connects on loopback regardless
// of how the listener was configured (prevents accidental bridge-network routing
// when a container misconfigures GOCELL_HTTP_INTERNAL_ADDR=0.0.0.0:9090).
// Used to construct the ConfigClient base URL from SharedDeps.InternalHTTPAddr.
func internalAddrToBaseURL(addr string) string {
	if addr == "" {
		return "http://127.0.0.1:9090"
	}
	if strings.HasPrefix(addr, ":") {
		return "http://127.0.0.1" + addr
	}
	// Normalise 0.0.0.0:port → 127.0.0.1:port (defense against misconfiguration).
	if strings.HasPrefix(addr, "0.0.0.0:") {
		return "http://127.0.0.1:" + strings.TrimPrefix(addr, "0.0.0.0:")
	}
	return "http://" + addr
}

func resolveAdminProvisionMode(raw string, forceBootstrap bool) (adminProvisionMode, error) {
	switch strings.TrimSpace(raw) {
	case "", string(adminProvisionModeInteractive):
		if forceBootstrap {
			return adminProvisionModeBootstrap, nil
		}
		return adminProvisionModeInteractive, nil
	case string(adminProvisionModeBootstrap):
		return adminProvisionModeBootstrap, nil
	default:
		return "", errcode.NewInfra(errcode.ErrCellInvalidConfig,
			fmt.Sprintf("%s must be one of: interactive, bootstrap; got %q", AdminProvisionModeEnv, raw))
	}
}
