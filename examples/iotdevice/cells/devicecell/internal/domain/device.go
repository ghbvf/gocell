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
// Certificate-renewal state (CertEpoch / CertExpiresAt / RenewalRequestedEpoch)
// is persisted on the row (#1819): the renewal reconcile loop scans CertExpiresAt
// for near-expiry certs and uses RenewalRequestedEpoch for cross-tick per-epoch
// dedup, so the state survives a restart in durable (PG) mode.
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
	// (0 = none yet). The scan skips a cert while RenewalRequestedEpoch ==
	// CertEpoch, so each epoch is requested at most once across reconcile ticks.
	RenewalRequestedEpoch int64
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
	// ListCertificateRenewalCandidates returns every device whose certificate
	// expires at or before expiresBefore AND whose current epoch has not already
	// been renewal-requested (RenewalRequestedEpoch != CertEpoch), sorted by
	// CertExpiresAt ASC, DeviceID ASC. Devices with no issued cert (zero
	// CertExpiresAt) are excluded.
	ListCertificateRenewalCandidates(ctx context.Context, expiresBefore time.Time) ([]CertificateRenewalCandidate, error)
	// MarkCertRenewalRequested records that a rotate-cert command was enqueued
	// for deviceID at epoch. It is a compare-and-set on CertEpoch: a no-op when
	// the device is gone or its current epoch no longer matches (the cert was
	// re-issued between the scan and this mark) — the newer epoch is re-observed.
	MarkCertRenewalRequested(ctx context.Context, deviceID string, epoch int64) error
	// RepoReady verifies the devices table is reachable and readable.
	// Used by the cell-level readiness probe registered as "devicecell_repo_ready".
	RepoReady(ctx context.Context) error
}
