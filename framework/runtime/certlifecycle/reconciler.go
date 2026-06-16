package certlifecycle

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/reconcile"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/validation"
	"github.com/ghbvf/gocell/framework/runtime/certsigning"
)

// Policy holds the cert-lifecycle Reconciler tuning. Both fields must be
// positive; NewReconciler validates via Policy.validate().
type Policy struct {
	// MaxLookahead is the scan-cutoff window: ListRenewalCandidates returns certs
	// with NotAfter <= now+MaxLookahead. It must comfortably exceed 30% of the
	// longest certificate lifetime so no cert crosses its 70% jitter instant
	// un-scanned (the precise per-cert due decision is dueForRenewal; MaxLookahead
	// is only the coarse scan bound).
	MaxLookahead time.Duration
	// SignTTL is the requested validity of the renewed certificate. It must be
	// within the Authorizer's granted MaxTTL (NewAuthorizedCertRequest enforces
	// SignTTL <= grant.MaxTTL, else the request is denied).
	SignTTL time.Duration
}

func (p Policy) validate() error {
	if p.MaxLookahead <= 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"certlifecycle.Policy: MaxLookahead must be positive")
	}
	if p.SignTTL <= 0 {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"certlifecycle.Policy: SignTTL must be positive")
	}
	return nil
}

// Reconciler is the reusable server-side certificate-lifecycle reconcile.Reconciler.
// On each tick it sweeps every renewable certificate due for renewal (k8s 70–90%
// jitter), and for each: Authorize (fail-closed) → re-sign the device's STORED
// CSR via certsigning.Signer → persist the new generation through the fenced
// write surface. The persisted fenced mutation also carries the cert-issued L2
// fact, which the consumer's ApplyFenced writes ATOMICALLY with the row (the
// Reconciler never calls outbox.Emit — L2 atomicity requires one transaction,
// and the only transaction is the consumer's).
//
// It generalizes the iotdevice devicecertrenewal seed's reconcile loop SHAPE
// (stateless full sweep, level-triggered, returns Result{}), but changes the
// action from "enqueue a rotate-cert command (device self-renews)" to
// server-side signing.
//
// # At-most-once valid signing, multi-replica safety
//
// The Reconciler signs INLINE (softca signs synchronously in <10ms). Cross-replica
// correctness comes from leader-election (only the lease holder sweeps) plus the
// FencedWriter's monotonic LEASE-epoch CAS on the persist: if a handoff window
// briefly lets two leaders both sign, only the live-epoch Write is accepted; the
// zombie's stale-epoch Write is rejected (ErrFencedWriteStale) and produces no
// row change and no cert-issued event. The Reconciler does NOT reuse
// runtime/command — "reuse runtime/command" is satisfied at the reconcile-loop
// archetype level, and the queue active-uniqueness the issue mentions is realized
// structurally by the fencing CAS, not a command queue.
//
// # System identity / multi-tenant
//
// The reconcile Loop installs a tenantless system identity (#1821), so the
// Reconciler sources the tenant from each scanned Candidate row, never from ctx.
// The Loop's construction site declares reconcile.SingleTenant() or
// reconcile.TenantScoped(); the Candidate already carries TenantID so a
// multi-tenant sweep keeps tenants distinct.
type Reconciler struct {
	clk        clock.Clock
	repo       DeviceCertRepository
	signer     certsigning.Signer
	authorizer certsigning.Authorizer
	policy     Policy
	logger     *slog.Logger
}

// Compile-time proof the Reconciler satisfies the reconcile.Reconciler contract.
var _ reconcile.Reconciler = (*Reconciler)(nil)

// NewReconciler constructs the cert-lifecycle Reconciler. clk is a mandatory
// positional dependency; repo, signer and authorizer are required and fail fast
// when nil; policy fields must be positive; a nil logger falls back to
// slog.Default(). There is deliberately no emitter dependency: the cert-issued
// L2 fact is written transactionally inside the consumer's ApplyFenced.
func NewReconciler(
	clk clock.Clock,
	repo DeviceCertRepository,
	signer certsigning.Signer,
	authorizer certsigning.Authorizer,
	policy Policy,
	logger *slog.Logger,
) (*Reconciler, error) {
	clock.MustHaveClock(clk, "certlifecycle.NewReconciler")
	if validation.IsNilInterface(repo) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"certlifecycle.NewReconciler: repo must not be nil")
	}
	if validation.IsNilInterface(signer) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"certlifecycle.NewReconciler: signer must not be nil")
	}
	if validation.IsNilInterface(authorizer) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"certlifecycle.NewReconciler: authorizer must not be nil")
	}
	if err := policy.validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Reconciler{
		clk: clk, repo: repo, signer: signer, authorizer: authorizer,
		policy: policy, logger: logger,
	}, nil
}

// Reconcile sweeps every renewable certificate due for renewal. req.EntityID is
// ignored: the Loop is driven by a TickerTrigger whose resync-all sentinel (empty
// EntityID) means "re-observe every cert you own". The full sweep is bounded by
// the scan cutoff; the per-cert jitter decision is applied in reconcileOne.
func (r *Reconciler) Reconcile(ctx context.Context, _ reconcile.Request) (reconcile.Result, error) {
	// Fail-closed BEFORE any work: the renewal persist MUST be fenced. A Loop wired
	// without WithFencedRepo+WithLeader has no lease-scoped write surface, so
	// persisting would be unfenced. This is a wiring misconfiguration that every
	// tick would hit identically, so mark it PermanentError — the Loop dead-letters
	// it rather than burning indefinite transient backoff (fail-fast on misconfig).
	fw, ok := reconcile.FencedWriterFrom(ctx)
	if !ok {
		return reconcile.Result{}, reconcile.PermanentError(errcode.New(errcode.KindInternal, errcode.ErrInternal,
			"certlifecycle: no fenced writer in ctx — wire reconcile.WithFencedRepo + WithLeader"))
	}
	now := r.clk.Now()
	cutoff := now.Add(r.policy.MaxLookahead)
	candidates, err := r.repo.ListRenewalCandidates(ctx, cutoff)
	if err != nil {
		// Transient (DB/scan I/O may recover): errcode.Wrap stamps the Kind so the
		// Loop classifies it as retryable rather than guessing from a bare error.
		return reconcile.Result{}, errcode.Wrap(errcode.KindUnavailable, errcode.ErrInternal,
			"certlifecycle: scan near-expiry failed", err)
	}
	var firstErr error
	c := sweepCounts{total: len(candidates)}
	for _, cand := range candidates {
		if err := r.reconcileOne(ctx, fw, cand, now, &c); err != nil && firstErr == nil {
			// Bubble the first transient error so the Loop backs off and re-sweeps;
			// the fenced CAS makes a re-sweep idempotent. Per-candidate deny / stale
			// outcomes are handled inside reconcileOne and never returned.
			firstErr = err
		}
	}
	// Per-sweep summary: the reconcile Loop maps a nil return to result="success",
	// so deny / not-due / skipped outcomes are invisible in the framework metric.
	// This single Info line per sweep makes the renewed/denied/skipped/errored
	// distribution observable (e.g. a long-running all-denied state) without a
	// per-device (high-cardinality) metric.
	r.logger.Info("certlifecycle: swept cert renewals",
		slog.Int("candidates", c.total), slog.Int("renewed", c.renewed),
		slog.Int("denied", c.denied), slog.Int("skipped", c.skipped),
		slog.Int("errored", c.errored))
	return reconcile.Result{}, firstErr
}

// sweepCounts aggregates per-candidate outcomes of one Reconcile sweep for the
// summary log. denied counts authorization deny/error (the security-relevant
// "policy said no"); skipped counts benign non-renewals (not-due, non-renewable
// state, malformed row, constraint violation, lost fencing race); errored counts
// transient failures that also bubble to the Loop.
type sweepCounts struct {
	total, renewed, denied, skipped, errored int
}

// reconcileOne renews one candidate. It returns a non-nil (transient) error only
// for conditions worth backing off the whole sweep (scan-independent failures:
// signing failure, an unexpected fenced-write error). Deny, malformed-row, jitter
// not-due, non-renewable state, and stale-epoch rejection are logged and skipped
// (return nil) — they must not poison the rest of the sweep.
func (r *Reconciler) reconcileOne(ctx context.Context, fw reconcile.FencedWriter, cand Candidate, now time.Time, c *sweepCounts) error {
	if !cand.State.renewable() {
		r.logger.Debug("certlifecycle: skip non-renewable cert",
			slog.String("device_id", cand.DeviceID), slog.String("state", cand.State.String()))
		c.skipped++
		return nil
	}
	if !dueForRenewal(now, cand.NotBefore, cand.NotAfter, cand.DeviceID, cand.Serial) {
		c.skipped++
		return nil
	}
	if now.After(cand.NotAfter) {
		// Past notAfter but the stored CSR is still valid — re-sign to recover
		// (level-triggered convergence). Warn for observability; still renew.
		r.logger.Warn("certlifecycle: cert already expired — re-signing to recover",
			slog.String("device_id", cand.DeviceID), slog.Uint64("epoch", cand.Epoch),
			slog.Duration("expired_for", now.Sub(cand.NotAfter)))
	}
	return r.renew(ctx, fw, cand, c)
}

// renew runs the Authorize → Sign → fenced-persist pipeline for one candidate.
func (r *Reconciler) renew(ctx context.Context, fw reconcile.FencedWriter, cand Candidate, c *sweepCounts) error {
	scope, subject, err := buildScopeSubject(cand)
	if err != nil {
		// Malformed row data (e.g. non-canonical tenant) — retry won't fix it; skip
		// this candidate without poisoning the sweep.
		r.logger.Error("certlifecycle: skip candidate with invalid identity",
			slog.String("device_id", cand.DeviceID), slog.String("tenant", string(cand.TenantID)),
			slog.Any("err", err))
		c.skipped++
		return nil
	}
	grant, ok := r.authorize(ctx, scope, subject, cand)
	if !ok {
		c.denied++
		return nil // fail-closed deny: do NOT sign, existing cert untouched
	}
	authReq, ok := r.buildAuthorizedRequest(scope, subject, cand, grant)
	if !ok {
		c.skipped++
		return nil // request build / constraint violation: do NOT sign
	}
	issued, err := r.signer.Sign(ctx, authReq)
	if err != nil {
		// Signing failure: do NOT persist or emit; the existing certificate is
		// untouched. Log at Error in the certlifecycle namespace (device-level
		// visibility) and bubble transient so the Loop backs off (the CA may be down).
		r.logger.Error("certlifecycle: sign renewed certificate failed",
			slog.String("device_id", cand.DeviceID), slog.Any("err", err))
		c.errored++
		return errcode.Wrap(errcode.KindUnavailable, errcode.ErrCertSignFailed,
			"certlifecycle: sign renewed certificate failed", err)
	}
	return r.persist(ctx, fw, cand, issued, c)
}

// authorize evaluates the enrollment claim fail-closed. It returns ok=false (and
// logs) on any denial — an error, or a non-granted SignConstraints — so the
// caller skips signing.
func (r *Reconciler) authorize(
	ctx context.Context, scope certsigning.CertScope, subject certsigning.DeviceSubject, cand Candidate,
) (certsigning.SignConstraints, bool) {
	claim, err := certsigning.NewEnrollmentClaim(scope, subject)
	if err != nil {
		r.logger.Error("certlifecycle: build enrollment claim failed",
			slog.String("device_id", cand.DeviceID), slog.Any("err", err))
		return certsigning.SignConstraints{}, false
	}
	grant, err := r.authorizer.AuthorizeEnroll(ctx, claim)
	if err != nil {
		r.logger.Warn("certlifecycle: renewal authorization error — skipping",
			slog.String("device_id", cand.DeviceID), slog.Any("err", err))
		return certsigning.SignConstraints{}, false
	}
	if !grant.Granted() {
		r.logger.Warn("certlifecycle: renewal authorization denied — skipping",
			slog.String("device_id", cand.DeviceID))
		return certsigning.SignConstraints{}, false
	}
	return grant, true
}

// buildAuthorizedRequest rebuilds the CertRequest from the stored CSR and funnels
// it through NewAuthorizedCertRequest (TTL / SAN constraint enforcement). It
// returns ok=false (and logs) on a request-build or constraint failure.
func (r *Reconciler) buildAuthorizedRequest(
	scope certsigning.CertScope, subject certsigning.DeviceSubject, cand Candidate, grant certsigning.SignConstraints,
) (certsigning.AuthorizedCertRequest, bool) {
	sans, err := certsigning.NewSubjectAltNames(cand.DNSNames, cand.IPAddresses, cand.URIs)
	if err != nil {
		r.logger.Error("certlifecycle: build SANs failed",
			slog.String("device_id", cand.DeviceID), slog.Any("err", err))
		return certsigning.AuthorizedCertRequest{}, false
	}
	usages, err := certsigning.NewKeyUsages(x509.KeyUsageDigitalSignature, x509.ExtKeyUsageClientAuth)
	if err != nil {
		r.logger.Error("certlifecycle: build key usages failed",
			slog.String("device_id", cand.DeviceID), slog.Any("err", err))
		return certsigning.AuthorizedCertRequest{}, false
	}
	req, err := certsigning.NewCertRequest(scope, subject, cand.CSRDER, sans, usages, r.policy.SignTTL)
	if err != nil {
		// Stored CSR corrupt / POP invalid — retry won't fix it; skip.
		r.logger.Error("certlifecycle: rebuild cert request from stored CSR failed",
			slog.String("device_id", cand.DeviceID), slog.Any("err", err))
		return certsigning.AuthorizedCertRequest{}, false
	}
	authReq, err := certsigning.NewAuthorizedCertRequest(req, grant)
	if err != nil {
		r.logger.Warn("certlifecycle: renewal request outside granted constraints — skipping",
			slog.String("device_id", cand.DeviceID), slog.Any("err", err))
		return certsigning.AuthorizedCertRequest{}, false
	}
	return authReq, true
}

// persist writes the renewed certificate through the ONLY write surface — the
// epoch-bound FencedWriter. A stale-epoch rejection means another replica won the
// fencing race: do NOT retry and do NOT emit (the winner's fenced write carries
// the cert-issued fact). Any other write error is transient and bubbles.
func (r *Reconciler) persist(
	ctx context.Context, fw reconcile.FencedWriter, cand Candidate, issued certsigning.IssuedCert, c *sweepCounts,
) error {
	mut := &IssuedMutation{
		DeviceID:    cand.DeviceID,
		TenantID:    cand.TenantID,
		IssuerID:    cand.IssuerID,
		Serial:      issued.Serial().String(),
		TargetEpoch: cand.Epoch + 1,
		CertDER:     issued.DER(),
		ChainDER:    issued.Chain(),
		NotBefore:   issued.NotBefore(),
		NotAfter:    issued.NotAfter(),
		NewState:    StateActive(),
	}
	if err := fw.Write(ctx, cand.DeviceID, mut); err != nil {
		if errors.Is(err, reconcile.ErrFencedWriteStale) {
			r.logger.Info("certlifecycle: lost fencing race — another replica renewed",
				slog.String("device_id", cand.DeviceID), slog.Uint64("target_epoch", mut.TargetEpoch))
			c.skipped++
			return nil
		}
		c.errored++
		return errcode.Wrap(errcode.KindUnavailable, errcode.ErrInternal,
			"certlifecycle: persist renewed certificate failed", err)
	}
	c.renewed++
	r.logger.Info("certlifecycle: renewed certificate",
		slog.String("device_id", cand.DeviceID), slog.Uint64("epoch", mut.TargetEpoch),
		slog.String("serial", mut.Serial), slog.Time("not_after", mut.NotAfter))
	return nil
}

// buildScopeSubject derives the typed CertScope and DeviceSubject from the
// candidate row. The tenant comes from cand.TenantID (the typed row value, never
// from ctx — #1821 the reconcile Loop runs under a tenantless system identity);
// NewCertScope / NewDeviceSubject validate it (canonical UUID) at construction.
func buildScopeSubject(cand Candidate) (certsigning.CertScope, certsigning.DeviceSubject, error) {
	issuer, err := certsigning.NewIssuerID(cand.IssuerID)
	if err != nil {
		return certsigning.CertScope{}, certsigning.DeviceSubject{}, fmt.Errorf("issuer: %w", err)
	}
	device, err := certsigning.NewDeviceID(cand.DeviceID)
	if err != nil {
		return certsigning.CertScope{}, certsigning.DeviceSubject{}, fmt.Errorf("device: %w", err)
	}
	scope, err := certsigning.NewCertScope(cand.TenantID, issuer, device)
	if err != nil {
		return certsigning.CertScope{}, certsigning.DeviceSubject{}, fmt.Errorf("scope: %w", err)
	}
	subject, err := certsigning.NewDeviceSubject(cand.TenantID, device, cand.CommonName)
	if err != nil {
		return certsigning.CertScope{}, certsigning.DeviceSubject{}, fmt.Errorf("subject: %w", err)
	}
	return scope, subject, nil
}
