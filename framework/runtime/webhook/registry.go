package webhook

import (
	"fmt"
	"net/http"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/idempotency"
	kwh "github.com/ghbvf/gocell/framework/kernel/webhook"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/validation"
)

// BuildRouteGroups converts a slice of [cell.WebhookReceiverRequest] values
// (accumulated by the cell registry during Init) into [cell.RouteGroup] values
// ready for bootstrap to mount onto the HTTP server.
//
// Each request produces exactly one RouteGroup. Construction of the HMAC
// verifier and [Receiver] for each request happens eagerly so configuration
// errors are reported at startup rather than at request time.
//
// clk is the mandatory positional clock (CLOCK-POSITIONAL-INJECTION-01); it
// is the first parameter per the GoCell clock-injection convention.
// store and claimer are shared across all receivers; they must not be nil.
// rec is the optional receive-side metrics recorder (the zero value disables
// recording); the bootstrap auto-wire passes the registered collector when a
// metrics provider is configured.
func BuildRouteGroups(
	clk clock.Clock,
	reqs []cell.WebhookReceiverRequest,
	store kwh.SourceStore,
	claimer idempotency.Claimer,
	rec kwh.Metrics,
) ([]cell.RouteGroup, error) {
	clock.MustHaveClock(clk, "webhook.BuildRouteGroups")
	if validation.IsNilInterface(store) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook BuildRouteGroups: store must not be nil")
	}
	if validation.IsNilInterface(claimer) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrWebhookConfigInvalid,
			"webhook BuildRouteGroups: claimer must not be nil")
	}

	groups := make([]cell.RouteGroup, 0, len(reqs))
	for _, req := range reqs {
		rg, err := buildRouteGroup(clk, req, store, claimer, rec)
		if err != nil {
			return nil, fmt.Errorf("webhook BuildRouteGroups: contract %q: %w",
				req.Spec.ContractID, err)
		}
		groups = append(groups, rg)
	}
	return groups, nil
}

// buildRouteGroup builds a single RouteGroup from one WebhookReceiverRequest.
//
// clk is the first parameter per CLOCK-POSITIONAL-INJECTION-01.
// Prefix carries the full PathPattern for cell ownership attribution in the
// HTTP metrics cell label. Register mounts at "/" so that the bootstrap adapter
// composes joinPrefix(Prefix, "/") = Prefix — avoiding a double-prefix when
// the adapter walks RouteGroup.Prefix + the Mount argument.
func buildRouteGroup(
	clk clock.Clock,
	req cell.WebhookReceiverRequest,
	store kwh.SourceStore,
	claimer idempotency.Claimer,
	rec kwh.Metrics,
) (cell.RouteGroup, error) {
	tolerance := time.Duration(req.Spec.ToleranceSeconds) * time.Second
	verifier, err := kwh.NewHMACVerifier(clk, kwh.WithTolerance(tolerance))
	if err != nil {
		return cell.RouteGroup{}, fmt.Errorf("build verifier: %w", err)
	}

	recv, err := NewReceiver(clk, req.Spec, verifier, store, claimer, req.Handler, WithMetrics(rec))
	if err != nil {
		return cell.RouteGroup{}, fmt.Errorf("build receiver: %w", err)
	}

	// Capture recv for the closure (loop variable capture is safe in Go 1.22+
	// but we capture explicitly for clarity).
	handler := http.Handler(recv)
	pattern := req.Spec.PathPattern

	// Prefix carries the full path pattern for cell ownership attribution.
	// Register mounts at "/" so the adapter composes
	// joinPrefix(Prefix, "/") = Prefix — no double-prefix.
	//
	// Listener is the dedicated cell.WebhookListener (NOT PrimaryListener):
	// inbound webhooks authenticate via HMAC inside the Receiver, so the listener
	// auth chain is auth.AuthNone{}. Mounting on PrimaryListener (which typically
	// carries a JWT chain) would 401 an HMAC-signed request before it reached the
	// verifier. The composition root MUST configure
	// WithListener(cell.WebhookListener, addr, []auth.ListenerAuth{auth.AuthNone{}});
	// bootstrap fail-fasts if a webhook RouteGroup names an unconfigured listener.
	return cell.RouteGroup{
		Listener: cell.WebhookListener,
		Prefix:   pattern,
		CellID:   req.Spec.CellID,
		Register: func(mux cell.RouteMux) error {
			mux.Mount("/", handler)
			return nil
		},
	}, nil
}
