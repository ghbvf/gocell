// Package mem provides in-memory implementations of the device domain repositories.
package mem

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
)

// DeviceRepository is a thread-safe in-memory device store.
type DeviceRepository struct {
	mu      sync.RWMutex
	devices map[string]*domain.Device
}

// Compile-time interface check.
var _ domain.DeviceRepository = (*DeviceRepository)(nil)

// NewDeviceRepository creates an empty in-memory DeviceRepository.
func NewDeviceRepository() *DeviceRepository {
	return &DeviceRepository{devices: make(map[string]*domain.Device)}
}

// Create stores a new device. Returns an error if the ID already exists.
func (r *DeviceRepository) Create(_ context.Context, device *domain.Device) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.devices[device.ID]; exists {
		return errcode.New(errcode.KindConflict, errcode.ErrConflict,
			"device already exists",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("id=%q", device.ID))))
	}
	stored := *device
	stored.NormalizeCertState() // single source: backfill zero cert_epoch -> 1 (matches PG CHECK)
	r.devices[device.ID] = &stored
	return nil
}

// GetByID retrieves a device by ID.
func (r *DeviceRepository) GetByID(_ context.Context, id string) (*domain.Device, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	d, ok := r.devices[id]
	if !ok {
		return nil, errcode.New(errcode.KindNotFound, errcode.ErrDeviceNotFound,
			"device not found",
			errcode.WithDetails(errcode.PublicString("deviceId", id)))
	}
	out := *d
	return &out, nil
}

// List returns paginated devices sorted and cursor-filtered per params.
func (r *DeviceRepository) List(_ context.Context, params query.ListParams) ([]*domain.Device, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	all := make([]*domain.Device, 0, len(r.devices))
	for _, d := range r.devices {
		cp := *d
		all = append(all, &cp)
	}

	query.Sort(all, params.Sort, compareDeviceField)
	result, err := query.ApplyCursor(all, params, deviceFieldValue)
	if err != nil {
		return nil, fmt.Errorf("device-repo: list: %w", err)
	}
	return result, nil
}

// RepoReady always returns nil for the in-memory store (no external dependency).
// It satisfies domain.DeviceRepository and healthz.RepoProber.
func (r *DeviceRepository) RepoReady(_ context.Context) error { return nil }

// ListCertificateRenewalCandidates returns ALL near-expiry certs
// (cert_expires_at <= cutoff, non-zero), sorted by expiry then id. No limit is
// applied — the reconciler sweeps the full set on each tick. Dedup and retry
// throttle are delegated to the command queue active-uniqueness (#1820); this
// scan is purely a cert-expiry predicate.
func (r *DeviceRepository) ListCertificateRenewalCandidates(
	_ context.Context, cutoff time.Time,
) ([]domain.CertificateRenewalCandidate, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]domain.CertificateRenewalCandidate, 0)
	for _, d := range r.devices {
		if d.CertExpiresAt.IsZero() || d.CertExpiresAt.After(cutoff) {
			continue // no cert issued, or not yet near expiry
		}
		out = append(out, domain.CertificateRenewalCandidate{
			DeviceID:      d.ID,
			CertEpoch:     d.CertEpoch,
			CertExpiresAt: d.CertExpiresAt,
		})
	}
	slices.SortFunc(out, func(a, b domain.CertificateRenewalCandidate) int {
		if c := a.CertExpiresAt.Compare(b.CertExpiresAt); c != 0 {
			return c
		}
		return cmp.Compare(a.DeviceID, b.DeviceID)
	})
	return out, nil
}

// AdvanceCertAfterRotation CAS-advances a device's cert state on a successful
// rotate-cert ack (#1870): if the device exists AND is still at rotatedEpoch, it
// moves to rotatedEpoch+1 with cert_expires_at=newExpiry and returns advanced=true.
// A stale epoch (already advanced) or an unknown device matches nothing and
// returns advanced=false with a nil error — the idempotent no-op that makes
// replayed / duplicate acks safe. Mirrors the PG partial-CAS UPDATE.
func (r *DeviceRepository) AdvanceCertAfterRotation(
	_ context.Context, deviceID string, rotatedEpoch int64, newExpiry time.Time,
) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	d, ok := r.devices[deviceID]
	if !ok || d.CertEpoch != rotatedEpoch {
		return false, nil // unknown device or already-advanced epoch: idempotent no-op
	}
	d.CertEpoch = rotatedEpoch + 1
	d.CertExpiresAt = newExpiry
	return true, nil
}

func compareDeviceField(a, b *domain.Device, field string) int {
	switch field {
	case "name":
		return cmp.Compare(a.Name, b.Name)
	case "id":
		return cmp.Compare(a.ID, b.ID)
	case "status":
		return cmp.Compare(a.Status, b.Status)
	default:
		return 0
	}
}

func deviceFieldValue(d *domain.Device, field string) any {
	switch field {
	case "name":
		return d.Name
	case "id":
		return d.ID
	case "status":
		return d.Status
	default:
		return ""
	}
}
