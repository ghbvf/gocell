// Package authorizationdecide implements the authorization-decide slice: the
// ABAC PDP (policy decision point) engine. It loads every policy owned by the
// request tenant, evaluates each rule's attribute conditions against the
// request's subject/environment attributes, applies deny-overrides (forbid-wins)
// combining, and returns a sealed authz.Decision. It implements
// runtime/auth.Authorizer.
//
// # Combining algorithm (default-deny + forbid-wins)
//
// All policies of the tenant are evaluated (condition-based applicability:
// PR-6 #1344 deliberately modeled only Subject/Resource/Environment condition
// sources and NO first-class action/resource-type Rule.Target, so PR-7 resolves
// applicability THROUGH conditions rather than a separate target-matching phase;
// design decision recorded in research.md §1.1). For each rule whose conditions
// all match: a Deny effect makes the whole decision Deny regardless of any
// matching permits (XACML §7.16 deny-overrides / Cedar forbid-wins). With no
// matching Deny and at least one matching permit, the decision is Allow carrying
// the combined obligations. With no matching rule at all, the decision is the
// default Deny.
//
// # Fail-closed
//
// authz.Allow / authz.Deny are constructed ONLY in evaluator.go and ONLY for a
// genuine policy verdict. Every infrastructure failure path (missing tenant,
// policy-store error) returns (authz.Decision{}, err): the zero Decision is
// non-Allow by construction (pkg/authz/doc.go), so the engine can never emit an
// Allow on an error path. A condition referencing a missing attribute evaluates
// to false (see attributes.go resolve → found=false), so a permit rule that
// depends on an absent attribute never grants — missing attribute is fail-closed
// by construction.
package authorizationdecide

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/abac"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/scopedtx"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/authz"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/tenant"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/auth"
)

// Compile-time check: Service implements auth.Authorizer.
var _ auth.Authorizer = (*Service)(nil)

// Service is the ABAC policy evaluation engine (PDP).
type Service struct {
	policyRepo    ports.PolicyRepository          `gocell:"required" gocellErr:"authorizationdecide: policyRepo is required"`                                                                         //nolint:lll // R2-approved: struct tag for required-dep funnel cannot be split
	resourceAttrs ports.ResourceAttributeProvider `gocell:"required" gocellErr:"authorizationdecide: resourceAttrs is required"`                                                                      //nolint:lll // R2-approved: struct tag for required-dep funnel cannot be split
	txRunner      persistence.CellTxManager       `gocell:"required" gocellKind:"KindInvalid" gocellCode:"ErrValidationFailed" gocellErr:"authorizationdecide: TxRunner required; use WithTxManager"` //nolint:lll // R2-approved: struct tag for required-dep funnel cannot be split
	clk           clock.Clock                     `gocell:"required" gocellErr:"authorizationdecide.NewService: clock.Clock required"`                                                                //nolint:lll // R2-approved: struct tag for required-dep funnel cannot be split
	logger        *slog.Logger
}

// Option configures Service.
type Option func(*Service)

// WithTxManager injects a CellTxManager. Typed-nil inputs are ignored; the
// subsequent factory call will fail with ErrValidationFailed.
func WithTxManager(tx persistence.CellTxManager) Option {
	return func(s *Service) {
		if !validation.IsNilInterface(tx) {
			s.txRunner = tx
		}
	}
}

// NewService creates the ABAC authorization-decide engine. clk is the mandatory
// injected clock used to resolve environment time attributes (no time.Now() in
// this package; AUTHZ-EVAL-CLOCK-INJECTED-01). resourceAttrs is the PIP
// (Policy Information Point) source for RESOURCE-category attributes; it is
// fetched inside the same tenant-scoped tx block as policy loading so the two
// reads share a single tenant binding (RESOURCE-ATTR-TENANT-SHARING-01).
// Returns an error when policyRepo, resourceAttrs, or txRunner is nil
// (typed-nil or bare). logger defaults to slog.Default() when nil.
func NewService(
	clk clock.Clock,
	policyRepo ports.PolicyRepository,
	resourceAttrs ports.ResourceAttributeProvider,
	logger *slog.Logger,
	opts ...Option,
) (*Service, error) {
	clock.MustHaveClock(clk, "authorizationdecide.NewService")
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{policyRepo: policyRepo, resourceAttrs: resourceAttrs, clk: clk, logger: logger}
	for _, o := range opts {
		o(s)
	}
	if err := s.validateRequired(); err != nil {
		return nil, err
	}
	return s, nil
}

// Authorize evaluates the request against the tenant's ABAC policy set and
// returns a sealed authz.Decision. See the package and auth.Authorizer godoc for
// the fail-closed return contract: infrastructure failures return
// (authz.Decision{}, err) — never an Allow — and the error's errcode Kind drives
// the HTTP status (KindUnavailable → 503 on store failure).
//
// resource is the resourceID passed to ResourceAttributeProvider.GetAttributes
// inside the same tenant-scoped tx block as policy loading, ensuring policy
// load and resource attribute fetch share a single tenant binding
// (RESOURCE-ATTR-TENANT-SHARING-01). action is carried for observability.
func (s *Service) Authorize(ctx context.Context, subject, resource, action string) (authz.Decision, error) {
	tid, err := tenant.FromContext(ctx)
	if err != nil {
		// No tenant scope on the request: errcode-classified
		// (KindPermissionDenied → 403). Zero Decision is fail-closed.
		return authz.Decision{}, fmt.Errorf("authorization-decide: tenant: %w", err)
	}

	// evalInputs carries the two data sources fetched inside the tenant-scoped
	// block: all tenant policies + the resource attributes for this request's
	// resource. Both are loaded within the same scopedtx.Do call so they share
	// one RLS GUC binding (RESOURCE-ATTR-TENANT-SHARING-01).
	type evalInputs struct {
		policies      []*abac.Policy
		resourceAttrs map[string][]string
	}

	inputs, err := scopedtx.Do(ctx, s.txRunner, tid, func(txCtx context.Context) (evalInputs, error) {
		pols, polErr := s.policyRepo.ListByTenant(txCtx, tid)
		if polErr != nil {
			return evalInputs{}, polErr
		}
		resAttrs, attrErr := s.resourceAttrs.GetAttributes(txCtx, resource, tid)
		if attrErr != nil {
			return evalInputs{}, attrErr
		}
		return evalInputs{policies: pols, resourceAttrs: resAttrs}, nil
	})
	if err != nil {
		// Store unreachable: fail-closed. KindUnavailable → 503; the
		// cause is carried for server-side logging and stripped from the wire.
		return authz.Decision{}, errcode.Wrap(errcode.KindUnavailable, errcode.ErrServiceUnavailable,
			"authorization-decide: policy store unavailable", err)
	}

	// An authenticated principal is required. Without one the PDP must not decide:
	// an unconditional permit (zero conditions) or an environment-only permit
	// never resolves a subject attribute, so it would otherwise grant an
	// unauthenticated request. Fail closed BEFORE evaluation rather than relying
	// on per-condition subject resolution (which those rule shapes skip).
	principal, ok := auth.FromContext(ctx)
	if !ok || principal == nil {
		return authz.Decision{}, errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden,
			"authorization-decide: no authenticated principal")
	}

	// Subject attributes come from the authenticated principal (trusted JWT
	// claims, FR-012); environment attributes from the injected clock;
	// resource attributes from the injected ResourceAttributeProvider (PR-9).
	resolver := attributeResolver{
		principal:     principal,
		now:           s.clk.Now(),
		resourceAttrs: inputs.resourceAttrs,
	}

	baseline := builtinBaselineRules()
	dec := s.evaluate(inputs.policies, resolver, action)
	s.logger.Debug(
		"authorization decision",
		slog.String("subject", subject),
		slog.String("resource", resource),
		slog.String("action", action),
		slog.Bool("allowed", dec.IsAllow()),
		slog.Int("policy_count", len(inputs.policies)),
		slog.Int("baseline_count", len(baseline)),
	)
	return dec, nil
}
