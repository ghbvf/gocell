// Package domain defines the core domain model for the devicecell example.
package domain

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/pkg/query"
)

// DefaultCertEpoch is the epoch a device's certificate starts at on first issue.
// Real epochs start at 1 so the zero value never collides with a live epoch and
// the renewal_requested_epoch zero ("none requested yet") is unambiguous. It is
// the single source shared by deviceregister (initial issue), NormalizeCertState
// (zero-value backfill) and the devices_cert_epoch_positive CHECK (cert_epoch >= 1).
const DefaultCertEpoch int64 = 1

// Device represents an IoT device aggregate.
//
// Certificate-renewal state (CertEpoch / CertExpiresAt / RenewalRequestedEpoch /
// RenewalRequestedAt) is persisted on the row (#1819, #1820): the renewal
// reconcile loop scans CertExpiresAt for near-expiry certs and uses
// RenewalRequestedEpoch + RenewalRequestedAt for per-epoch dedup and time-window
// retry release, so the state survives a restart in durable (PG) mode.
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
	// RenewalRequestedEpoch is the epoch a renewal command was last enqueued for
	// (0 = none yet). Used together with RenewalRequestedAt for the time-window
	// retry release: a new epoch (RenewalRequestedEpoch != CertEpoch) makes the
	// device immediately eligible; the same epoch is eligible again once
	// RenewalRequestedAt falls outside the retry window.
	RenewalRequestedEpoch int64
	// RenewalRequestedAt is the wall-clock time a renewal command was last
	// enqueued for the current epoch. Zero value means the current epoch was
	// never requested (or the row predates migration 060 — the IS-NULL/zero
	// migration bridge re-includes such rows as candidates).
	//
	// Forward-state invariant: RenewalRequestedEpoch == CertEpoch ⟹
	// RenewalRequestedAt non-zero. Maintained atomically by
	// MarkCertRenewalRequested. Persisted nullable: zero <-> SQL NULL.
	RenewalRequestedAt time.Time
}

// NormalizeCertState backfills any CertEpoch below DefaultCertEpoch (e.g. the
// zero value) to DefaultCertEpoch so a bare Device{} persists consistently in
// every repository (mem + PG) and satisfies the devices_cert_epoch_positive CHECK
// (cert_epoch >= 1). It is the single source for the epoch default and is
// clock-free: CertExpiresAt is intentionally left as-is (zero -> unset/NULL), and
// RenewalRequestedEpoch zero is a valid "none yet". Repository Create
// implementations MUST call it before persisting; in practice a missing call is
// caught loudly — the conformance suite's bare-Device{} case asserts the backfill,
// and the PG cert_epoch >= 1 CHECK rejects any row that slips through.
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
	// ListCertificateRenewalCandidates returns near-expiry certs
	// (cert_expires_at <= expiresBefore, non-zero) that are renewal-ELIGIBLE:
	// either the current epoch was never requested
	// (RenewalRequestedEpoch != CertEpoch), OR the last request is
	// stale/missing (RenewalRequestedAt is zero OR RenewalRequestedAt <=
	// retryBefore — the time-window retry release). Sorted by CertExpiresAt
	// ASC, DeviceID ASC, capped at limit (limit must be > 0; the cheapest
	// candidates by expiry come first). Devices with no issued cert (zero
	// CertExpiresAt) are excluded.
	ListCertificateRenewalCandidates(
		ctx context.Context,
		expiresBefore time.Time,
		retryBefore time.Time,
		limit int,
	) ([]CertificateRenewalCandidate, error)
	// MarkCertRenewalRequested is a compare-and-set on CertEpoch: records BOTH
	// RenewalRequestedEpoch=epoch AND RenewalRequestedAt=requestedAt. Re-marking
	// the same epoch (a time-window retry) is allowed and refreshes
	// RenewalRequestedAt. No-op when the device is gone or its current epoch no
	// longer matches (the cert was re-issued between the scan and this mark —
	// the newer epoch is re-observed on the next tick).
	MarkCertRenewalRequested(ctx context.Context, deviceID string, epoch int64, requestedAt time.Time) error
	// RepoReady verifies the devices table is reachable and readable.
	// Used by the cell-level readiness probe registered as "devicecell_repo_ready".
	RepoReady(ctx context.Context) error
}
