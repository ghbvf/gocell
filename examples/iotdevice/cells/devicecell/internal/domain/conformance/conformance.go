// Package conformance provides a shared behavioral suite for
// domain.DeviceRepository implementations (mem + PG). The intent is that any
// new implementation enrolls against the same set of t.Run sub-tests so
// behavior stays in lock-step without resorting to per-impl t.Skip.
//
// ref: cells/accesscore/internal/ports/conformance (Factory + Features pattern)
package conformance

import (
	"context"
	"errors"
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
	t.Run("CertRenewalCandidates/NearExpiryOnly", func(t *testing.T) {
		runCertRenewalCandidatesNearExpiry(t, factory, features)
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

func device(id, name string, now time.Time) *domain.Device {
	return &domain.Device{
		ID:            id,
		Name:          name,
		Status:        "online",
		LastSeen:      now,
		CertEpoch:     domain.DefaultCertEpoch,
		CertExpiresAt: now.Add(domain.DefaultCertTTL),
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

	d := device("dev-1", "Edge-01", now())
	createDevice(t, ctx, repo, tx, features, d)

	got, err := repo.GetByID(ctx, "dev-1")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.ID != "dev-1" || got.Name != "Edge-01" || got.Status != "online" {
		t.Fatalf("unexpected device: %+v", got)
	}
	if got.CertEpoch != domain.DefaultCertEpoch || got.CertExpiresAt.IsZero() {
		t.Fatalf("unexpected cert state: %+v", got)
	}
}

func runCreateDuplicate(t *testing.T, factory DeviceRepoFactory, features Features) {
	t.Helper()
	repo, tx, now, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	nowTime := now()
	d := device("dup", "first", nowTime)
	createDevice(t, ctx, repo, tx, features, d)

	err := inTx(t, ctx, tx, features, func(c context.Context) error {
		return repo.Create(c, device("dup", "second", nowTime))
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

	createDevice(t, ctx, repo, tx, features, device("g-1", "n", now()))
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

	nowTime := now()
	createDevice(t, ctx, repo, tx, features, device("id-1", "Charlie", nowTime))
	createDevice(t, ctx, repo, tx, features, device("id-2", "Alpha", nowTime))
	createDevice(t, ctx, repo, tx, features, device("id-3", "Bravo", nowTime))

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

	nowTime := now()
	createDevice(t, ctx, repo, tx, features, device("p-1", "A", nowTime))
	createDevice(t, ctx, repo, tx, features, device("p-2", "B", nowTime))
	createDevice(t, ctx, repo, tx, features, device("p-3", "C", nowTime))

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
		createDevice(t, ctx, repo, tx, features, device(s.id, s.name, now()))
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
// Certificate renewal candidates
// ---------------------------------------------------------------------------

func runCertRenewalCandidatesNearExpiry(t *testing.T, factory DeviceRepoFactory, features Features) {
	t.Helper()
	repo, tx, now, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()
	base := now()
	cutoff := base.Add(7 * 24 * time.Hour)

	near := device("cert-a", "near", base)
	near.CertEpoch = 2
	near.CertExpiresAt = cutoff.Add(-time.Hour)
	atCutoff := device("cert-b", "edge", base)
	atCutoff.CertEpoch = 3
	atCutoff.CertExpiresAt = cutoff
	later := device("cert-c", "later", base)
	later.CertEpoch = 4
	later.CertExpiresAt = cutoff.Add(time.Hour)

	createDevice(t, ctx, repo, tx, features, later)
	createDevice(t, ctx, repo, tx, features, atCutoff)
	createDevice(t, ctx, repo, tx, features, near)

	got, err := repo.ListCertificateRenewalCandidates(ctx, cutoff)
	if err != nil {
		t.Fatalf("ListCertificateRenewalCandidates: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 renewal candidates, got %d: %+v", len(got), got)
	}
	if got[0].DeviceID != "cert-a" || got[0].CertEpoch != 2 {
		t.Fatalf("candidate[0] = %+v, want cert-a epoch 2", got[0])
	}
	if got[1].DeviceID != "cert-b" || got[1].CertEpoch != 3 {
		t.Fatalf("candidate[1] = %+v, want cert-b epoch 3", got[1])
	}
}
