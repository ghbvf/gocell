// cell_providers.go hosts AccessCore's "exposed service" provider methods
// — accessors that other layers (runtime/auth middleware, bootstrap health
// probes) consume to wire cross-cutting concerns. Routing and event
// subscription wiring live in cell_routes.go; constructor + options live
// in cell.go; Init() + slice construction lives in cell_init.go.
//
// This split closes A5a-R5 CELL-ROUTES-PROVIDERS-SPLIT-01 as part of
// PR-A5c OUTBOX-EMITTER-UNIFY (PR #245); it mirrors the cell.go physical
// split introduced in PR-A5a (PR #234) so a reader can locate a concern
// by file name alone. See:
//   - docs/plans/202604232330-025-architecture-pr-implementation-plan.md
//     (PR-A5c entry, retroactively marks A5a-R5 ✅ delivered via PR #245)
package accesscore

import (
	"context"
	"log/slog"

	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/auth"
)

// HealthCheckers implements cell.HealthContributor. Returns named readiness
// probes for internal components. Bootstrap auto-discovers this interface
// and registers probes in /readyz.
//
// Currently exposes "session-store" when the session repo implements
// ports.HealthCheckable. Both in-memory and real adapters implement
// HealthCheckable, so the probe is present in all modes. Returns an
// empty map only when sessionRepo is nil (no repo injected at all).
//
// The emitter checker (outbox-failopen-rate.accesscore) is enabled by default
// at a 5% threshold; it returns cell.ErrDegraded when the fail-open drop ratio
// sustained between two /readyz probes exceeds that threshold. Disable via
// outbox.WithFailOpenRateThreshold(0) when constructing the emitter.
func (c *AccessCore) HealthCheckers() map[string]func(context.Context) error {
	checkers := make(map[string]func(context.Context) error)
	if hc, ok := c.sessionRepo.(ports.HealthCheckable); ok {
		checkers["session-store"] = func(ctx context.Context) error {
			return hc.Health(ctx)
		}
	}
	// PR-A49: aggregate emitter (DirectEmitter) HealthContributor checkers.
	// The /readyz aggregator surfaces fail-open drop ratio degraded signals
	// via cell.ErrDegraded → HTTP 200 + status="degraded".
	if hc, ok := c.emitter.(cell.HealthContributor); ok {
		for k, v := range hc.HealthCheckers() {
			if _, dup := checkers[k]; dup {
				slog.Error("accesscore: duplicate health checker name; emitter checker dropped",
					slog.String("checker", k),
					slog.String("source", "outbox-emitter"))
				continue
			}
			checkers[k] = v
		}
	}
	return checkers
}

// TokenVerifier returns the session-validate service. It satisfies
// auth.IntentTokenVerifier so it can be plugged into AuthMiddleware without
// a runtime type assertion.
func (c *AccessCore) TokenVerifier() auth.IntentTokenVerifier {
	if c.validateSvc == nil {
		return nil
	}
	return c.validateSvc
}

// Authorizer returns the authorization-decide service (implements auth.Authorizer).
func (c *AccessCore) Authorizer() auth.Authorizer {
	return c.authzSvc
}
