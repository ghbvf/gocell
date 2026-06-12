// Package domain defines the core domain model for the devicecell example.
package domain

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/pkg/query"
)

// DefaultCertEpoch is the epoch a device's certificate starts at on first issue.
// Real epochs start at 1 so the zero value never collides with a live epoch and
// the deterministic rotate-cert command id (cert-rotate:<device>:<epoch>) is
// unambiguous per certificate generation. It is the single source shared by
// deviceregister (initial issue), NormalizeCertState (zero-value backfill) and
// the devices_cert_epoch_positive CHECK (cert_epoch >= 1).
const DefaultCertEpoch int64 = 1

// CertValidity is the lifetime of a freshly-issued device certificate, and the
// SINGLE SOURCE for both the initial issue (deviceregister) and the post-rotation
// re-issue (devicecertcompletion advances cert_expires_at to resolvedAt+CertValidity
// on a successful rotate-cert ack, #1870). Centralizing it here makes the two
// issuance sites impossible to drift.
//
// INVARIANT (correctness, not just tuning): CertValidity MUST exceed the cell's
// cert-renewal near-expiry Threshold (certRenewalThreshold in cell.go). If it did
// not, a freshly-rotated cert would land inside the near-expiry window again and
// the completion write would NOT remove the device from the candidate set — the
// L4 convergence loop would never close. The cell Init asserts this at startup
// (fail-fast); the two values live in separate packages (cell vs domain) to avoid
// a slice→cell import cycle, so the startup guard is what keeps them co-tuned.
const CertValidity = 90 * 24 * time.Hour

// Device represents an IoT device aggregate.
//
// Certificate-renewal state (CertEpoch / CertExpiresAt) is persisted on the
// row (#1819): the renewal reconcile loop scans CertExpiresAt for near-expiry
// certs. Correctness ("at most one active rotate-cert per (device,epoch)") is
// owned by the command queue via active-uniqueness (#1820); un-executed commands
// self-expire via OverallDeadline and are retried on the next reconcile tick.
type Device struct {
	ID       string
	Name     string
	Status   string // online, offline
	LastSeen time.Time

	// CertEpoch is a monotonic per-device counter advanced on each (re-)issue;
	// it makes the rotate-cert command id deterministic per certificate
	// generation. Always >= DefaultCertEpoch once persisted (NormalizeCertState).
	CertEpoch int64
	// CertExpiresAt is the certificate's NotAfter. The zero value means "no cert
	// issued" — such a row is skipped by ListCertificateRenewalCandidates. It is
	// persisted nullable: zero <-> SQL NULL.
	CertExpiresAt time.Time
}

// NormalizeCertState backfills any CertEpoch below DefaultCertEpoch (e.g. the
// zero value) to DefaultCertEpoch so a bare Device{} persists consistently in
// every repository (mem + PG) and satisfies the devices_cert_epoch_positive CHECK
// (cert_epoch >= 1). It is the single source for the epoch default and is
// clock-free: CertExpiresAt is intentionally left as-is (zero -> unset/NULL).
// Repository Create implementations MUST call it before persisting; in practice
// a missing call is caught loudly — the conformance suite's bare-Device{} case
// asserts the backfill, and the PG cert_epoch >= 1 CHECK rejects any row that
// slips through.
func (d *Device) NormalizeCertState() {
	if d.CertEpoch < DefaultCertEpoch {
		d.CertEpoch = DefaultCertEpoch
	}
}

// CertificateRenewalCandidate is the projection ListCertificateRenewalCandidates
// returns: the minimal cert state the renewal reconciler needs to build and
// deduplicate a rotate-cert command.
type CertificateRenewalCandidate struct {
	DeviceID      string
	CertEpoch     int64
	CertExpiresAt time.Time
}

// DeviceRepository abstracts device persistence.
type DeviceRepository interface {
	Create(ctx context.Context, device *Device) error
	GetByID(ctx context.Context, id string) (*Device, error)
	// List returns a paginated list of devices sorted by name ASC, id ASC.
	List(ctx context.Context, params query.ListParams) ([]*Device, error)
	// ListCertificateRenewalCandidates returns ALL near-expiry certs
	// (cert_expires_at <= cutoff, non-zero), sorted by CertExpiresAt ASC,
	// DeviceID ASC. Devices with no issued cert (zero CertExpiresAt) are
	// excluded. No LIMIT is applied — the reconciler sweeps the full set on
	// each tick; the queue active-uniqueness owns dedup and retry throttle
	// (#1820); this scan is purely a cert-expiry predicate.
	ListCertificateRenewalCandidates(
		ctx context.Context,
		cutoff time.Time,
	) ([]CertificateRenewalCandidate, error)
	// AdvanceCertAfterRotation records an observed cert rotation completion
	// (#1870): when a device acks a rotate-cert command as succeeded, the cell's
	// completion consumer CAS-advances the device row from rotatedEpoch to
	// rotatedEpoch+1 and sets cert_expires_at to newExpiry (the new generation's
	// NotAfter). It is a PURE observed-state write (controller-runtime status vs
	// spec separation): it touches only cert_epoch + cert_expires_at, never policy.
	//
	// The CAS predicate (WHERE cert_epoch = rotatedEpoch) makes the write
	// idempotent under at-least-once delivery and duplicate device acks: a replay
	// for an epoch the device has already advanced past — or for a device that no
	// longer exists — matches no row and returns advanced=false with a nil error,
	// so the consumer can Ack the duplicate without corrupting state. advanced=true
	// means exactly one row was advanced. newExpiry must be a non-zero future time.
	AdvanceCertAfterRotation(
		ctx context.Context,
		deviceID string,
		rotatedEpoch int64,
		newExpiry time.Time,
	) (advanced bool, err error)
	// RepoReady verifies the devices table is reachable and readable.
	// Used by the cell-level readiness probe registered as "devicecell_repo_ready".
	RepoReady(ctx context.Context) error
}
