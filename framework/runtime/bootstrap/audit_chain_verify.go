package bootstrap

// audit_chain_verify.go — the framework-owned admin audit chain verify endpoint
// (#1755).
//
//	POST /admin/v1/audit/chains/verify
//
// Mounted by bootstrap itself (not a cell) on the AdminListener — the same
// framework-owned-RouteGroup pattern as projection_rebuild.go and the health
// endpoints: no contract.yaml, no codegen, no host cell. Opt-in via
// WithAuditChainVerifyEndpoint(); phase5CollectRouteGroups appends the RouteGroup
// when enabled, and phase0 (validateAuditChainVerifyEndpoint) guarantees both an
// AdminListener and an injected verifier exist.
//
// This is an operator→system action (an administrator / ops tool triggers a full
// per-(namespace, tenant) audit chain integrity verify), NOT a cell→cell call.
// Authentication is the AdminListener's operator-credential gate (AuthOperator:
// env credentials + per-IP rate limit over a loopback port); the framework
// ContractSpec carries no Clients.
//
// Synchronous: the handler runs the whole verify (bounded by the orchestrator's
// 30s timeout, mirroring startup tail-verify) and returns a per-chain report. A
// run that COMPLETES — even with tampered chains — is HTTP 200 with allValid=false;
// alerting is driven by the audit_chain_verify_invalid_chains gauge + the
// per-chain slog.Error the orchestrator emits. Only a run-level failure (enumerate
// / infra error from VerifyAll) maps to a framework 5xx.

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/httputil"
	"github.com/ghbvf/gocell/framework/runtime/audit"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/framework/runtime/internal/contractbuild"
)

// chainVerifier is the narrow control-plane surface the audit chain verify handler
// consumes — only "run a full verify and return an aggregate report". Declared here
// (the consumer) per the accept-interfaces-at-the-consumer idiom, keeping the
// handler unit-testable with a fake. *audit.ChainVerifier satisfies it.
type chainVerifier interface {
	VerifyAll(ctx context.Context) (audit.ChainVerifyReport, error)
}

// Compile-time assertion that *audit.ChainVerifier satisfies chainVerifier.
var _ chainVerifier = (*audit.ChainVerifier)(nil)

const (
	auditChainVerifyContractID = "http.framework.audit.chains.verify.v1"
	auditChainVerifyPath       = "/admin/v1/audit/chains/verify"
)

// auditChainFailure is one tampered/errored chain in the 200 response. It carries
// CHAIN IDENTITY + verdict scalars only — never audit row content (mirrors
// audit.ChainVerifyResult's no-content field set). For an errored chain the infra
// reason stays in the server log (slog), never on the wire.
type auditChainFailure struct {
	Namespace       string `json:"namespace"`
	TenantID        string `json:"tenantId"`
	TailSeq         int64  `json:"tailSeq"`
	FirstInvalidSeq int64  `json:"firstInvalidSeq"`
	MissingGenesis  bool   `json:"missingGenesis"`
	Errored         bool   `json:"errored"` // true → verify could not complete (infra/misconfig), see server log
}

// auditChainVerifyResponseData is the data object of the 200 response body. Valid
// chains are summarized by count; only invalid/errored chains are listed in
// failures, so the body stays bounded by the number of problems (usually zero).
// timedOut indicates the 30s budget truncated the run; unverifiedChains counts the
// chains the run never reached (counted, NOT listed — keeping the body bounded), so
// allValid is false whenever any chain went unverified.
type auditChainVerifyResponseData struct {
	AllValid         bool                `json:"allValid"`
	TotalChains      int                 `json:"totalChains"`
	InvalidChains    int                 `json:"invalidChains"`
	ErroredChains    int                 `json:"erroredChains"`
	UnverifiedChains int                 `json:"unverifiedChains"`
	TimedOut         bool                `json:"timedOut"`
	DurationMs       int64               `json:"durationMs"`
	Failures         []auditChainFailure `json:"failures"`
}

// auditChainVerifyResponse is the unified {"data": {...}} single-resource envelope.
type auditChainVerifyResponse struct {
	Data auditChainVerifyResponseData `json:"data"`
}

// validateAuditChainVerifyEndpoint fails fast in phase0 when the endpoint was
// opted in (WithAuditChainVerifyEndpoint) but its prerequisites are missing: an
// AdminListener to mount on, and an injected verifier to serve. Not opting in is a
// no-op. Mirrors validateProjectionRebuildEndpoint.
func (b *Bootstrap) validateAuditChainVerifyEndpoint() error {
	if !b.auditChainVerifyEnabled {
		return nil
	}
	if _, ok := b.listenerConfigs[cell.AdminListener]; !ok {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"bootstrap: WithAuditChainVerifyEndpoint requires a cell.AdminListener; "+
				"declare it via WithListener(cell.AdminListener, addr, []kauth.ListenerAuth{operatorAuth})")
	}
	if b.auditChainVerifier == nil {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"bootstrap: WithAuditChainVerifyEndpoint requires an audit chain verifier; "+
				"inject it via WithAuditChainVerifier (the composition root builds it from the admin pool)")
	}
	return nil
}

// auditChainVerifyRouteGroup builds the framework-owned RouteGroup for the verify
// endpoint on the AdminListener. Only called when enabled + phase0 verified the
// AdminListener + verifier exist. No caller-cell allowlist (operator→system).
func (b *Bootstrap) auditChainVerifyRouteGroup() cell.RouteGroup {
	spec := contractbuild.NewFrameworkHTTP(
		auditChainVerifyContractID, http.MethodPost, auditChainVerifyPath)
	handler := b.newAuditChainVerifyHandler()
	return cell.RouteGroup{
		Listener: cell.AdminListener,
		Register: func(mux cell.RouteMux) error {
			return auth.Mount(mux, auth.Route{Contract: spec, Handler: handler})
		},
	}
}

// newAuditChainVerifyHandler returns the http.Handler. It captures the receiver so
// it reads b.auditChainVerifier as wired by WithAuditChainVerifier.
func (b *Bootstrap) newAuditChainVerifyHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		report, err := b.auditChainVerifier.VerifyAll(ctx)
		if err != nil {
			// Run-level failure (enumerate / infra) — VerifyAll returns a coded
			// error and has already slog.Error'd the cause. Map to the framework
			// 5xx; the per-chain detail never reaches the wire.
			httputil.WriteError(ctx, w, err)
			return
		}

		data := auditChainVerifyResponseData{
			AllValid:         report.AllValid(),
			TotalChains:      report.TotalChains,
			InvalidChains:    report.InvalidChains,
			ErroredChains:    report.ErroredChains,
			UnverifiedChains: report.UnverifiedChains,
			TimedOut:         report.TimedOut,
			DurationMs:       report.Duration.Milliseconds(),
			Failures:         collectAuditChainFailures(report),
		}

		attrs := httputil.AppendCorrelationAttrs(ctx, []any{
			slog.Bool("all_valid", data.AllValid),
			slog.Int("total_chains", data.TotalChains),
			slog.Int("invalid_chains", data.InvalidChains),
			slog.Int("errored_chains", data.ErroredChains),
			slog.Int("unverified_chains", data.UnverifiedChains),
		})
		slog.InfoContext(ctx, "audit chain verify endpoint served", attrs...)

		httputil.WriteJSON(w, http.StatusOK, auditChainVerifyResponse{Data: data})
	})
}

// collectAuditChainFailures projects only the tampered/errored chains into the wire
// failure list (valid chains are summarized by count → bounded body).
func collectAuditChainFailures(report audit.ChainVerifyReport) []auditChainFailure {
	failures := make([]auditChainFailure, 0)
	for _, res := range report.Results {
		if res.Err == nil && res.Valid {
			continue
		}
		failures = append(failures, auditChainFailure{
			Namespace:       res.Namespace,
			TenantID:        res.TenantID,
			TailSeq:         res.TailSeq,
			FirstInvalidSeq: res.FirstInvalidSeq,
			MissingGenesis:  res.MissingGenesis,
			Errored:         res.Err != nil,
		})
	}
	return failures
}
