package certlifecycle

import (
	"context"
	"net"
	"net/url"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/reconcile"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
)

// Candidate is the projection [DeviceCertRepository.ListRenewalCandidates]
// returns: everything the Reconciler needs to make the renewal decision and
// rebuild a certsigning.CertRequest, WITHOUT reading the reconcile ctx for the
// tenant. The reconcile Loop installs a tenantless system identity at its
// chokepoint (#1821), so the tenant dimension MUST come from the scanned row
// (TenantID below), never from ctx.
type Candidate struct {
	// DeviceID is the cert subject device. It is NOT the fenced-write key on its
	// own: the FencedWriter entity key is the composite Candidate.EntityKey
	// (tenant|issuer|device) so a multi-tenant sweep never collides two tenants
	// that share a deviceID on one epoch namespace.
	DeviceID string
	// TenantID is the isolation domain of this certificate, sourced from the row
	// (NOT ctx). Typed (tenancy.md FR-003: repo/service APIs carry typed tenant,
	// not a bare string) so the consumer must parse/validate at the row boundary;
	// it flows into the CertScope/DeviceSubject and the cert-issued event so a
	// multi-tenant reconciler keeps tenants distinct.
	TenantID tenant.TenantID
	// IssuerID identifies the issuing CA for the scope.
	IssuerID string
	// Serial is the current certificate's serial — the stable per-certificate
	// identity for the deterministic renewal jitter (so each generation jitters to
	// its own instant) and for diagnostics.
	Serial string
	// Epoch is the current certificate generation (monotonic per device). The
	// renewed generation is Epoch+1.
	Epoch uint64
	// NotBefore / NotAfter is the current certificate's validity window, used by
	// the deterministic jitter decision (70–90% lifetime model).
	NotBefore time.Time
	NotAfter  time.Time
	// State is the current lifecycle state. The Reconciler renews only active
	// certificates (State.renewable); revoked / pre-active rows are skipped.
	State State
	// CommonName is the X.509 subject CN for the renewed certificate.
	CommonName string
	// CSRDER is the device's STORED PKCS#10 CSR (DER). Server-side renewal
	// re-signs this same CSR (same public key) with a fresh validity window — the
	// device's private key never leaves the device. NewCertRequest re-verifies its
	// proof-of-possession at construction.
	CSRDER []byte
	// DNSNames / IPAddresses / URIs are the requested subject alternative names,
	// rebuilt into a certsigning.SubjectAltNames for the renewal request.
	DNSNames    []string
	IPAddresses []net.IP
	URIs        []*url.URL
}

// EntityKey is the reconcile FencedWriter entity key for this candidate's
// certificate — the full isolation identity tenant|issuer|device, NOT the bare
// DeviceID. Encoding all three keeps the monotonic lease-epoch CAS namespace
// PER-CERTIFICATE so a TenantScoped reconciler never lets two tenants that share
// a deviceID collide on one epoch space (where one tenant's renewal would
// stale-reject the other's). For a SingleTenant deployment the deviceID is
// already globally unique, so the extra dimensions are harmless. The three
// components are validated identifiers (canonical-UUID tenant, NewIssuerID /
// NewDeviceID-shaped issuer and device), none of which can contain the '|'
// separator, so the encoding is unambiguous. This is also the entityID the
// consumer's ApplyFenced receives.
func (c Candidate) EntityKey() string {
	return string(c.TenantID) + "|" + c.IssuerID + "|" + c.DeviceID
}

// IssuedMutation is the payload of the Reconciler's single fenced write. It
// carries BOTH the persisted certificate material AND every field of the
// event.deviceidentity.cert-issued.v1 L2 fact, because the consumer's
// [DeviceCertRepository.ApplyFenced] persists the certificate row and writes the
// cert-issued outbox entry ATOMICALLY in one local transaction (L2 = local tx +
// outbox). The Reconciler never calls outbox.Emit itself: the only transaction
// is the consumer's, and emitting outside it would break L2 atomicity (a crash
// between a separate persist and emit would drop the at-least-once event). This
// is the FencedRepository-sanctioned "device writes / command emission from
// inside Reconcile" via the single fenced write surface.
//
// The cert-issued action for a Reconciler-driven write is always "renewed" (the
// Reconciler only renews; enrollment is the EST path), so it is not a field here
// — the consumer stamps Action=renewed when ApplyFenced is driven by this
// reconciler.
type IssuedMutation struct {
	// DeviceID / TenantID / IssuerID / Serial identify the issued certificate for
	// both the row and the cert-issued event's certRef. TenantID is typed
	// (tenancy.md FR-003) so the consumer's ApplyFenced writes a validated tenant.
	DeviceID string
	TenantID tenant.TenantID
	IssuerID string
	Serial   string
	// ActorID is the principal that triggered issuance — a REQUIRED field of the
	// cert-issued payload schema. Server-side renewal has no request principal, so
	// the Reconciler stamps the system actor ("system"), which matches the
	// tenantless system identity the reconcile Loop installs on the ctx (#1821):
	// the payload's actorId therefore equals the outbox envelope's principal
	// actorId by construction. The consumer maps this onto the generated payload's
	// actorId; the framework module cannot import the generated type, so it must
	// ride on the mutation rather than being derived consumer-side.
	ActorID string
	// TargetEpoch is the renewed generation (Candidate.Epoch+1) — the cert epoch
	// the row advances to and the cert-issued event's epoch. NOTE: this is the
	// CERT epoch, distinct from the LEASE epoch the FencedWriter passes to
	// ApplyFenced as the fencing token (see ApplyFenced).
	TargetEpoch uint64
	// CertDER / ChainDER is the newly minted certificate and its intermediate
	// chain (DER), persisted on the row.
	CertDER  []byte
	ChainDER [][]byte
	// NotBefore / NotAfter is the renewed validity window, persisted on the row
	// and carried on the cert-issued event.
	NotBefore time.Time
	NotAfter  time.Time
	// NewState is the row's lifecycle state after the renewal — StateActive: the
	// renewed generation is immediately active (a renewed cert is not a terminal
	// "rotated" row; rotated/expired are vocabulary, not persisted by renewal).
	NewState State
}

// DeviceCertRepository is the consumer-implemented persistence + scan seam for
// the cert-lifecycle Reconciler. It has exactly two methods, which makes the
// fenced ApplyFenced the STRUCTURAL single write surface — there is no other
// write method the Reconciler could call to bypass the fencing CAS.
//
// It also IS a reconcile.FencedRepository (it declares ApplyFenced with the
// reconcile signature), so the construction site passes the same value to both
// the Reconciler (for ListRenewalCandidates) and reconcile.New(...).
// WithFencedRepo(repo) (so the Loop mints the epoch-bound FencedWriter over it).
// The Reconciler NEVER calls ApplyFenced directly
// (RECONCILE-FENCED-WRITE-FUNNEL-01): it writes exclusively through
// reconcile.FencedWriterFrom(ctx).Write — declaring the method is legal; calling
// it from outside the sanctioned funnel is not.
type DeviceCertRepository interface {
	// ListRenewalCandidates returns certificates whose NotAfter is at or before
	// cutoff AND whose State is renewable (active), in a deterministic order
	// (NotAfter ascending, then DeviceID), capped at limit rows (the SQL LIMIT).
	// The scan is bounded on BOTH axes: the cutoff bounds WHICH certs are due, and
	// limit bounds HOW MANY a single sweep loads + signs, so a large backlog cannot
	// load and serially sign every candidate in one tick. The Reconciler drains a
	// full batch across sweeps (it self-requeues when the returned slice fills the
	// limit). The precise per-cert 70–90% jitter decision is applied by the
	// Reconciler, not the scan. The implementation returns only State=active rows:
	// near-expiry / renewing are computed in-memory by the Reconciler and never
	// persisted, so the consumer does NOT write those states.
	ListRenewalCandidates(ctx context.Context, cutoff time.Time, limit int) ([]Candidate, error)

	// ApplyFenced is the reconcile.FencedRepository monotonic-epoch CAS. The Loop
	// (via FencedWriter) calls it with the LEASE epoch as the fencing token; the
	// implementation MUST apply the write only when epoch >= the highest lease
	// epoch seen for entityID (advancing it), returning accepted=false (NOT an
	// error) on a stale epoch so a zombie leader's late write is rejected.
	//
	// CONTRACT (L2 atomicity — load-bearing): mutation is an *IssuedMutation; the
	// implementation type-asserts it and, in ONE local transaction, persists the
	// certificate row (advancing the CERT epoch to IssuedMutation.TargetEpoch and
	// the state to NewState) AND writes the event.deviceidentity.cert-issued.v1 L2
	// outbox entry (Action=renewed). This single-transaction co-write IS the L2
	// guarantee: an implementation that writes the row but NOT the outbox entry (or
	// writes them in separate transactions) silently downgrades the lifecycle to
	// L1 — the cert-issued event is lost with no retry path once the transaction
	// commits. The Reconciler cannot do this co-write itself (the only transaction
	// is the consumer's, and the framework module cannot import the generated
	// contract types), so it carries every cert-issued field on IssuedMutation and
	// relies on this contract.
	//
	// The two epochs are distinct monotonic counters: the LEASE epoch (this param)
	// fences cross-replica ordering; the CERT epoch (mutation.TargetEpoch) tracks
	// renewal generations on the row.
	reconcile.FencedRepository
}

// Compile-time proof that DeviceCertRepository embeds the fenced-repository seam,
// so a value implementing it can be wired via reconcile.WithFencedRepo.
var _ reconcile.FencedRepository = (DeviceCertRepository)(nil)
