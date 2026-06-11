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

// ListCertificateRenewalCandidates returns near-expiry certs whose current epoch
// has not yet been renewal-requested, sorted by expiry then id. See the
// domain.DeviceRepository contract.
func (r *DeviceRepository) ListCertificateRenewalCandidates(
	_ context.Context, expiresBefore time.Time,
) ([]domain.CertificateRenewalCandidate, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]domain.CertificateRenewalCandidate, 0)
	for _, d := range r.devices {
		if d.CertExpiresAt.IsZero() || d.CertExpiresAt.After(expiresBefore) {
			continue // no cert issued, or not yet near expiry
		}
		if d.RenewalRequestedEpoch == d.CertEpoch {
			continue // renewal already requested for the current epoch
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

// MarkCertRenewalRequested is a compare-and-set on CertEpoch: it records the
// renewal-requested epoch only while the device's current epoch still matches.
// See the domain.DeviceRepository contract.
func (r *DeviceRepository) MarkCertRenewalRequested(_ context.Context, deviceID string, epoch int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	d, ok := r.devices[deviceID]
	if !ok || d.CertEpoch != epoch {
		return nil // re-issued or gone since the scan; the new epoch will be re-observed
	}
	d.RenewalRequestedEpoch = epoch
	return nil
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
