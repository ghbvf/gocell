// Package mem provides in-memory implementations of the device domain repositories.
package mem

import (
	"cmp"
	"context"
	"fmt"
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

// ListCertificateRenewalCandidates returns devices whose current cert expires
// before or exactly at expiresBefore, ordered by cert expiry then device id.
func (r *DeviceRepository) ListCertificateRenewalCandidates(
	_ context.Context, expiresBefore time.Time,
) ([]domain.CertificateRenewalCandidate, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]domain.CertificateRenewalCandidate, 0)
	for _, d := range r.devices {
		if d.CertExpiresAt.IsZero() || d.CertExpiresAt.After(expiresBefore) {
			continue
		}
		out = append(out, domain.CertificateRenewalCandidate{
			DeviceID:      d.ID,
			CertEpoch:     d.CertEpoch,
			CertExpiresAt: d.CertExpiresAt,
		})
	}
	compare := func(a, b domain.CertificateRenewalCandidate) int {
		if c := a.CertExpiresAt.Compare(b.CertExpiresAt); c != 0 {
			return c
		}
		return cmp.Compare(a.DeviceID, b.DeviceID)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && compare(out[j], out[j-1]) < 0; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

// RepoReady always returns nil for the in-memory store (no external dependency).
// It satisfies domain.DeviceRepository and healthz.RepoProber.
func (r *DeviceRepository) RepoReady(_ context.Context) error { return nil }

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
