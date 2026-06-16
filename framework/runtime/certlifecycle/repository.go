package certlifecycle

import (
	"context"
	"net"
	"net/url"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/reconcile"
)

// Candidate is the projection [DeviceCertRepository.ListRenewalCandidates]
// returns: everything the Reconciler needs to make the renewal decision and
// rebuild a certsigning.CertRequest, WITHOUT reading the reconcile ctx for the
// tenant. The reconcile Loop installs a tenantless system identity at its
// chokepoint (#1821), so the tenant dimension MUST come from the scanned row
// (TenantID below), never from ctx.
type Candidate struct {
	// DeviceID is the entity locator: the FencedWriter Write key and the cert
	// subject device.
	DeviceID string
	// TenantID is the isolation domain of this certificate, sourced from the row
	// (NOT ctx). It flows into the CertScope/DeviceSubject and the cert-issued
	// event so a multi-tenant reconciler keeps tenants distinct.
	TenantID string
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
	// both the row and the cert-issued event's certRef.
	DeviceID string
	TenantID string
	IssuerID string
	Serial   string
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
	// ListRenewalCandidates returns all certificates whose NotAfter is at or
	// before cutoff AND whose State is renewable (active), in a deterministic
	// order (NotAfter ascending, then DeviceID). It is a bounded full sweep per
	// tick (the cutoff bounds the scan; there is no LIMIT) — the precise per-cert
	// 70–90% jitter decision is applied by the Reconciler, not the scan.
	ListRenewalCandidates(ctx context.Context, cutoff time.Time) ([]Candidate, error)

	// ApplyFenced is the reconcile.FencedRepository monotonic-epoch CAS. The Loop
	// (via FencedWriter) calls it with the LEASE epoch as the fencing token; the
	// implementation MUST apply the write only when epoch >= the highest lease
	// epoch seen for entityID (advancing it), returning accepted=false (NOT an
	// error) on a stale epoch so a zombie leader's late write is rejected. mutation
	// is an *IssuedMutation; the implementation type-asserts it and, in ONE local
	// transaction, persists the certificate row (advancing the CERT epoch to
	// IssuedMutation.TargetEpoch and the state to NewState) AND writes the
	// cert-issued L2 outbox entry. The two epochs are distinct monotonic counters:
	// the LEASE epoch (this param) fences cross-replica ordering; the CERT epoch
	// (mutation.TargetEpoch) tracks renewal generations on the row.
	reconcile.FencedRepository
}

// Compile-time proof that DeviceCertRepository embeds the fenced-repository seam,
// so a value implementing it can be wired via reconcile.WithFencedRepo.
var _ reconcile.FencedRepository = (DeviceCertRepository)(nil)
