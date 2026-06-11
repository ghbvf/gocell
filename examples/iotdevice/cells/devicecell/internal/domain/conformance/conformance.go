// Package conformance provides a shared behavioral suite for
// domain.DeviceRepository implementations (mem + PG). The intent is that any
// new implementation enrolls against the same set of t.Run sub-tests so
// behavior stays in lock-step without resorting to per-impl t.Skip.
//
// ref: corecells/accesscore/internal/ports/conformance (Factory + Features pattern)
package conformance

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
)

// fmtListErr is the t.Fatalf format string for List errors (3 sites).
const fmtListErr = "List: %v"

// DeviceRepoFactory builds a fresh DeviceRepository and its matching TxRunner.
// cleanup MUST be idempotent and safe to call even if no resources were
// acquired. now returns the suite's clock — implementations using wall time
// should return time.Now-equivalent so test timestamps stay reproducible.
type DeviceRepoFactory func(t *testing.T) (
	repo domain.DeviceRepository,
	txRunner persistence.TxRunner,
	now func() time.Time,
	cleanup func(),
)

// Features captures behavioral differences between in-memory and durable
// implementations.
type Features struct {
	// RequiresAmbientTx indicates writes must run inside a txRunner.RunInTx
	// scope (true for PG; false for mem). When true the suite wraps each
	// mutating call accordingly.
	RequiresAmbientTx bool
}

// RunDeviceRepoConformance drives the suite. Sub-tests scope failures.
func RunDeviceRepoConformance(t *testing.T, factory DeviceRepoFactory, features Features) {
	t.Helper()

	t.Run("Create/HappyPath", func(t *testing.T) { runCreateHappy(t, factory, features) })
	t.Run("Create/DuplicateID", func(t *testing.T) { runCreateDuplicate(t, factory, features) })
	t.Run("GetByID/HappyPath", func(t *testing.T) { runGetByIDHappy(t, factory, features) })
	t.Run("GetByID/NotFound", func(t *testing.T) { runGetByIDNotFound(t, factory, features) })
	t.Run("List/EmptyRepository", func(t *testing.T) { runListEmpty(t, factory, features) })
	t.Run("List/SortByNameASC", func(t *testing.T) { runListSortName(t, factory, features) })
	t.Run("List/Pagination", func(t *testing.T) { runListPagination(t, factory, features) })
	t.Run("List/SecondPage", func(t *testing.T) { runListSecondPage(t, factory, features) })
	t.Run("Cert/CreateBareDeviceNormalizes", func(t *testing.T) { runCreateBareDeviceNormalizes(t, factory, features) })
	t.Run("Cert/RenewalCandidatesNearExpiry", func(t *testing.T) { runCertRenewalCandidatesNearExpiry(t, factory, features) })
	t.Run("Cert/RenewalCandidatesReturnsAllNearExpiry", func(t *testing.T) {
		runCertRenewalCandidatesReturnsAllNearExpiry(t, factory, features)
	})
}

func inTx(t *testing.T, ctx context.Context, txRunner persistence.TxRunner, features Features, fn func(ctx context.Context) error) error {
	t.Helper()
	if !features.RequiresAmbientTx {
		return fn(ctx)
	}
	return txRunner.RunInTx(ctx, fn)
}

func createDevice(
	t *testing.T,
	ctx context.Context,
	repo domain.DeviceRepository,
	tx persistence.TxRunner,
	features Features,
	d *domain.Device,
) {
	t.Helper()
	if err := inTx(t, ctx, tx, features, func(c context.Context) error {
		return repo.Create(c, d)
	}); err != nil {
		t.Fatalf("createDevice %q: %v", d.ID, err)
	}
}

func errorCode(err error) errcode.Code {
	var ec *errcode.Error
	if errors.As(err, &ec) {
		return ec.Code
	}
	return ""
}

func requireErrCode(t *testing.T, err error, want errcode.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error %q, got nil", want)
	}
	if got := errorCode(err); got != want {
		t.Fatalf("expected errcode %q, got %q (err=%v)", want, got, err)
	}
}

// ---------------------------------------------------------------------------
// Create
// ---------------------------------------------------------------------------

func runCreateHappy(t *testing.T, factory DeviceRepoFactory, features Features) {
	t.Helper()
	repo, tx, now, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	d := &domain.Device{ID: "dev-1", Name: "Edge-01", Status: "online", LastSeen: now()}
	createDevice(t, ctx, repo, tx, features, d)

	got, err := repo.GetByID(ctx, "dev-1")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.ID != "dev-1" || got.Name != "Edge-01" || got.Status != "online" {
		t.Fatalf("unexpected device: %+v", got)
	}
}

func runCreateDuplicate(t *testing.T, factory DeviceRepoFactory, features Features) {
	t.Helper()
	repo, tx, now, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	d := &domain.Device{ID: "dup", Name: "first", Status: "online", LastSeen: now()}
	createDevice(t, ctx, repo, tx, features, d)

	err := inTx(t, ctx, tx, features, func(c context.Context) error {
		return repo.Create(c, &domain.Device{ID: "dup", Name: "second", Status: "online", LastSeen: now()})
	})
	requireErrCode(t, err, errcode.ErrConflict)
}

// ---------------------------------------------------------------------------
// GetByID
// ---------------------------------------------------------------------------

func runGetByIDHappy(t *testing.T, factory DeviceRepoFactory, features Features) {
	t.Helper()
	repo, tx, now, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	createDevice(t, ctx, repo, tx, features, &domain.Device{ID: "g-1", Name: "n", Status: "online", LastSeen: now()})
	got, err := repo.GetByID(ctx, "g-1")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.ID != "g-1" {
		t.Fatalf("expected ID g-1, got %s", got.ID)
	}
}

func runGetByIDNotFound(t *testing.T, factory DeviceRepoFactory, _ Features) {
	t.Helper()
	repo, _, _, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	_, err := repo.GetByID(ctx, "missing")
	requireErrCode(t, err, errcode.ErrDeviceNotFound)
}

// ---------------------------------------------------------------------------
// List
// ---------------------------------------------------------------------------

func runListEmpty(t *testing.T, factory DeviceRepoFactory, _ Features) {
	t.Helper()
	repo, _, _, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	params := query.ListParams{
		Limit: 20,
		Sort:  []query.SortColumn{{Name: "name", Direction: query.SortASC}, {Name: "id", Direction: query.SortASC}},
	}
	got, err := repo.List(ctx, params)
	if err != nil {
		t.Fatalf(fmtListErr, err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty list, got %d", len(got))
	}
}

func runListSortName(t *testing.T, factory DeviceRepoFactory, features Features) {
	t.Helper()
	repo, tx, now, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	createDevice(t, ctx, repo, tx, features, &domain.Device{ID: "id-1", Name: "Charlie", Status: "online", LastSeen: now()})
	createDevice(t, ctx, repo, tx, features, &domain.Device{ID: "id-2", Name: "Alpha", Status: "online", LastSeen: now()})
	createDevice(t, ctx, repo, tx, features, &domain.Device{ID: "id-3", Name: "Bravo", Status: "online", LastSeen: now()})

	params := query.ListParams{
		Limit: 20,
		Sort:  []query.SortColumn{{Name: "name", Direction: query.SortASC}, {Name: "id", Direction: query.SortASC}},
	}
	got, err := repo.List(ctx, params)
	if err != nil {
		t.Fatalf(fmtListErr, err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 devices, got %d", len(got))
	}
	if got[0].Name != "Alpha" || got[1].Name != "Bravo" || got[2].Name != "Charlie" {
		t.Fatalf("expected name ASC [Alpha, Bravo, Charlie], got [%s, %s, %s]", got[0].Name, got[1].Name, got[2].Name)
	}
}

func runListPagination(t *testing.T, factory DeviceRepoFactory, features Features) {
	t.Helper()
	repo, tx, now, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	createDevice(t, ctx, repo, tx, features, &domain.Device{ID: "p-1", Name: "A", Status: "online", LastSeen: now()})
	createDevice(t, ctx, repo, tx, features, &domain.Device{ID: "p-2", Name: "B", Status: "online", LastSeen: now()})
	createDevice(t, ctx, repo, tx, features, &domain.Device{ID: "p-3", Name: "C", Status: "online", LastSeen: now()})

	// Repo returns FetchLimit() = Limit+1 rows so the service can detect HasMore.
	params := query.ListParams{
		Limit: 2,
		Sort:  []query.SortColumn{{Name: "name", Direction: query.SortASC}, {Name: "id", Direction: query.SortASC}},
	}
	got, err := repo.List(ctx, params)
	if err != nil {
		t.Fatalf(fmtListErr, err)
	}
	// Both mem and PG honor FetchLimit() semantics: return up to Limit+1
	// rows so the service layer can compute HasMore. The conformance bar is
	// "at least Limit rows when Limit+1 are available".
	if len(got) < 2 {
		t.Fatalf("expected at least Limit=2 rows, got %d", len(got))
	}
	if got[0].Name != "A" || got[1].Name != "B" {
		t.Fatalf("expected [A, B] for first page, got [%s, %s]", got[0].Name, got[1].Name)
	}
}

// runListSecondPage verifies that CursorValues produces correct keyset
// pagination: second page skips the first page's rows and third page finds
// the remaining item. Tests the end-to-end cursor flow without codec encoding
// (CursorValues are supplied directly as decoded values).
func runListSecondPage(t *testing.T, factory DeviceRepoFactory, features Features) {
	t.Helper()
	repo, tx, now, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	sort := []query.SortColumn{
		{Name: "name", Direction: query.SortASC},
		{Name: "id", Direction: query.SortASC},
	}

	// Seed 5 devices: names A,B,C,D,E with matching IDs for stable ordering.
	seeds := []struct{ id, name string }{
		{"kp-1", "A"}, {"kp-2", "B"}, {"kp-3", "C"}, {"kp-4", "D"}, {"kp-5", "E"},
	}
	for _, s := range seeds {
		createDevice(t, ctx, repo, tx, features, &domain.Device{
			ID: s.id, Name: s.name, Status: "online", LastSeen: now(),
		})
	}

	// Page 1: no cursor.
	p1, err := repo.List(ctx, query.ListParams{Limit: 2, Sort: sort})
	if err != nil {
		t.Fatalf("List page1: %v", err)
	}
	// FetchLimit=3: expect 3 rows returned (A, B, C) — HasMore detected by service.
	if len(p1) < 2 {
		t.Fatalf("page1: expected at least 2 rows, got %d", len(p1))
	}
	if p1[0].Name != "A" || p1[1].Name != "B" {
		t.Fatalf("page1: expected [A, B], got [%s, %s]", p1[0].Name, p1[1].Name)
	}
	// Cursor is derived from the last visible item on page1 (B).
	lastVisible := p1[1]
	cursor2 := []any{lastVisible.Name, lastVisible.ID}

	// Page 2: keyset after (name="B", id="kp-2").
	p2, err := repo.List(ctx, query.ListParams{Limit: 2, Sort: sort, CursorValues: cursor2})
	if err != nil {
		t.Fatalf("List page2: %v", err)
	}
	if len(p2) < 2 {
		t.Fatalf("page2: expected at least 2 rows, got %d", len(p2))
	}
	if p2[0].Name != "C" || p2[1].Name != "D" {
		t.Fatalf("page2: expected [C, D], got [%s, %s]", p2[0].Name, p2[1].Name)
	}
	lastVisible2 := p2[1]
	cursor3 := []any{lastVisible2.Name, lastVisible2.ID}

	// Page 3: keyset after (name="D", id="kp-4") — only E remains.
	p3, err := repo.List(ctx, query.ListParams{Limit: 2, Sort: sort, CursorValues: cursor3})
	if err != nil {
		t.Fatalf("List page3: %v", err)
	}
	if len(p3) != 1 {
		t.Fatalf("page3: expected 1 row, got %d", len(p3))
	}
	if p3[0].Name != "E" {
		t.Fatalf("page3: expected [E], got [%s]", p3[0].Name)
	}
}

// ---------------------------------------------------------------------------
// Certificate-renewal state (#1819)
// ---------------------------------------------------------------------------

// runCreateBareDeviceNormalizes proves the F3 anti-fork guarantee: a bare
// Device{} (zero cert state) persists identically in mem and PG — CertEpoch
// backfilled to DefaultCertEpoch (clearing the cert_epoch >= 1 CHECK) and
// CertExpiresAt left unset (zero <-> NULL).
func runCreateBareDeviceNormalizes(t *testing.T, factory DeviceRepoFactory, features Features) {
	t.Helper()
	repo, tx, now, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	createDevice(t, ctx, repo, tx, features, &domain.Device{ID: "bare-1", Name: "bare", Status: "online", LastSeen: now()})

	got, err := repo.GetByID(ctx, "bare-1")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.CertEpoch != domain.DefaultCertEpoch {
		t.Fatalf("bare device CertEpoch = %d, want %d (normalized)", got.CertEpoch, domain.DefaultCertEpoch)
	}
	if !got.CertExpiresAt.IsZero() {
		t.Fatalf("bare device CertExpiresAt = %v, want zero (unset)", got.CertExpiresAt)
	}
}

// runCertRenewalCandidatesNearExpiry covers the scan filter + ordering: only
// certs expiring at/before the cutoff are returned, sorted by expiry ASC then
// id ASC. Devices with zero expiry (no cert issued) are excluded. Correctness
// (dedup, retry throttle) is delegated to the command queue active-uniqueness
// (#1820); this suite verifies the expiry-only predicate.
func runCertRenewalCandidatesNearExpiry(t *testing.T, factory DeviceRepoFactory, features Features) {
	t.Helper()
	repo, tx, now, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()
	base := now()
	cutoff := base.Add(30 * 24 * time.Hour)

	mk := func(id string, epoch int64, expiresAt time.Time) *domain.Device {
		return &domain.Device{
			ID: id, Name: id, Status: "online", LastSeen: base,
			CertEpoch: epoch, CertExpiresAt: expiresAt,
		}
	}
	// c-near: before cutoff -> candidate.
	createDevice(t, ctx, repo, tx, features, mk("c-near", 2, cutoff.Add(-time.Hour)))
	// c-at: exactly at cutoff -> candidate (<= is inclusive).
	createDevice(t, ctx, repo, tx, features, mk("c-at", 3, cutoff))
	// c-later: after cutoff -> excluded.
	createDevice(t, ctx, repo, tx, features, mk("c-later", 4, cutoff.Add(time.Hour)))
	// c-none: zero expiry (no cert issued) -> skipped.
	createDevice(t, ctx, repo, tx, features, mk("c-none", 1, time.Time{}))

	got, err := repo.ListCertificateRenewalCandidates(ctx, cutoff)
	if err != nil {
		t.Fatalf("ListCertificateRenewalCandidates: %v", err)
	}
	// Expected (expiry ASC): c-near (cutoff-1h), c-at (cutoff).
	// c-later excluded (after cutoff). c-none excluded (no cert).
	if len(got) != 2 {
		t.Fatalf("expected 2 candidates, got %d: %+v", len(got), got)
	}
	if got[0].DeviceID != "c-near" || got[0].CertEpoch != 2 {
		t.Fatalf("candidate[0] = %+v, want c-near epoch 2", got[0])
	}
	if got[1].DeviceID != "c-at" || got[1].CertEpoch != 3 {
		t.Fatalf("candidate[1] = %+v, want c-at epoch 3", got[1])
	}
}

// ---------------------------------------------------------------------------
// Certificate-renewal: full-sweep (no limit, #1820)
// ---------------------------------------------------------------------------

// runCertRenewalCandidatesReturnsAllNearExpiry verifies that
// ListCertificateRenewalCandidates returns ALL N near-expiry devices without
// any LIMIT truncation. The stateless producer sweeps the full set on every
// tick (#1820 full-sweep design); the queue active-uniqueness owns dedup.
func runCertRenewalCandidatesReturnsAllNearExpiry(t *testing.T, factory DeviceRepoFactory, features Features) {
	t.Helper()
	repo, tx, now, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()
	base := now()
	cutoff := base.Add(30 * 24 * time.Hour)

	const totalDevices = 5
	// Seed 5 near-expiry devices with distinct ascending expiries.
	for i := 0; i < totalDevices; i++ {
		expiry := cutoff.Add(-time.Duration(totalDevices-i) * time.Hour) // i=0 earliest
		d := &domain.Device{
			ID:            fmt.Sprintf("all-%d", i),
			Name:          fmt.Sprintf("all-%d", i),
			Status:        "online",
			LastSeen:      base,
			CertEpoch:     2,
			CertExpiresAt: expiry,
		}
		createDevice(t, ctx, repo, tx, features, d)
	}

	got, err := repo.ListCertificateRenewalCandidates(ctx, cutoff)
	if err != nil {
		t.Fatalf("ListCertificateRenewalCandidates: %v", err)
	}
	if len(got) != totalDevices {
		t.Fatalf("expected all %d results (no limit), got %d: %+v", totalDevices, len(got), got)
	}
	// All returned must be sorted by expiry ASC (all-0 earliest, all-4 latest).
	for idx, cand := range got {
		want := fmt.Sprintf("all-%d", idx)
		if cand.DeviceID != want {
			t.Fatalf("result[%d].DeviceID = %q, want %q (expiry ASC order)", idx, cand.DeviceID, want)
		}
	}
}
