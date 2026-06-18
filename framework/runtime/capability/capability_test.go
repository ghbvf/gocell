package capability

import (
	"context"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/outbox"
)

// fakeTxRunner / fakeWriter are minimal kernel-typed stand-ins so the test can
// assert NewPGProvider threads the injected primitives through unchanged.
type fakeTxRunner struct{}

func (fakeTxRunner) RunInTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return fn(ctx)
}

type fakeWriter struct{}

func (fakeWriter) Write(_ context.Context, _ outbox.Entry) error { return nil }

// Compile-time seal assertions: only package capability can satisfy the interfaces.
var (
	_ PGProvider    = pgProvider{}
	_ RedisProvider = redisProvider{}
	_ PGSet         = pgSet{}
)

// pgInst is a tiny helper to build a single-pool instance in the tests.
func pgInst(db any, cells ...string) PGInstance {
	return PGInstance{Provider: NewPGProvider(fakeTxRunner{}, fakeWriter{}, db), Cells: cells}
}

// TestPGSet_Colocated_AllCellsResolveSameProvider: colocated assemblies map every
// postgres cell to the SAME provider (one pool), so ForCell never misses and Sole
// reports that single provider — the projection harness's only sanctioned provider
// accessor (#2341 D1/D4).
func TestPGSet_Colocated_AllCellsResolveSameProvider(t *testing.T) {
	inst := pgInst("shared-pool", "accesscore", "auditcore", "configcore")
	set, err := NewPGSet([]PGInstance{inst})
	if err != nil {
		t.Fatalf("NewPGSet(colocated): %v", err)
	}
	for _, c := range []string{"accesscore", "auditcore", "configcore"} {
		got, err := set.ForCell(c)
		if err != nil {
			t.Fatalf("ForCell(%q): %v", c, err)
		}
		if got.DB() != "shared-pool" {
			t.Errorf("ForCell(%q).DB() = %v, want shared-pool (colocated: all cells share one pool)", c, got.DB())
		}
	}
	sole, ok := set.Sole()
	if !ok {
		t.Fatal("Sole(): want ok=true for colocated (1 distinct pool)")
	}
	if sole.DB() != "shared-pool" {
		t.Errorf("Sole().DB() = %v, want shared-pool", sole.DB())
	}
}

// TestPGSet_Split_DistinctProviderPerCell: split assemblies map each cell to its
// own pool's provider, and Sole reports ok=false (no single provider) — which the
// projection harness uses to fail-closed (cross-pool global_seq is incomparable).
func TestPGSet_Split_DistinctProviderPerCell(t *testing.T) {
	set, err := NewPGSet([]PGInstance{
		pgInst("pool-a", "accesscore"),
		pgInst("pool-b", "auditcore"),
	})
	if err != nil {
		t.Fatalf("NewPGSet(split): %v", err)
	}
	a, err := set.ForCell("accesscore")
	if err != nil || a.DB() != "pool-a" {
		t.Errorf("ForCell(accesscore) = (%v, %v), want pool-a", dbOf(a), err)
	}
	b, err := set.ForCell("auditcore")
	if err != nil || b.DB() != "pool-b" {
		t.Errorf("ForCell(auditcore) = (%v, %v), want pool-b", dbOf(b), err)
	}
	if _, ok := set.Sole(); ok {
		t.Error("Sole(): want ok=false for split (>1 distinct pool)")
	}
}

// TestPGSet_ForCell_MissFailsClosed: an unknown cell ID fails closed with the cell
// in the diagnostic — never a silent nil that would NPE downstream.
func TestPGSet_ForCell_MissFailsClosed(t *testing.T) {
	set, err := NewPGSet([]PGInstance{pgInst("pool", "configcore")})
	if err != nil {
		t.Fatalf("NewPGSet: %v", err)
	}
	got, err := set.ForCell("nonexistent")
	if err == nil {
		t.Fatal("ForCell(unknown): want fail-closed error, got nil")
	}
	if got != nil {
		t.Errorf("ForCell(unknown) provider = %v, want nil on error", got)
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("error = %q, want it to name the missing cell", err)
	}
}

// TestPGSet_DuplicateCell_Rejected: a cell mapped to two pools is a wiring bug and
// must be rejected at construction (fail-closed), never silently last-wins.
func TestPGSet_DuplicateCell_Rejected(t *testing.T) {
	_, err := NewPGSet([]PGInstance{
		pgInst("pool-a", "configcore"),
		pgInst("pool-b", "configcore"),
	})
	if err == nil {
		t.Fatal("NewPGSet(duplicate cell): want error, got nil")
	}
}

// TestPGSet_EmptyOrNilInstance_Rejected: an instance with no cells or a nil provider
// is a wiring bug; NewPGSet fails closed rather than building a half-wired set.
func TestPGSet_EmptyOrNilInstance_Rejected(t *testing.T) {
	if _, err := NewPGSet([]PGInstance{{Provider: nil, Cells: []string{"x"}}}); err == nil {
		t.Error("NewPGSet(nil provider): want error")
	}
	if _, err := NewPGSet([]PGInstance{{Provider: NewPGProvider(fakeTxRunner{}, fakeWriter{}, "p"), Cells: nil}}); err == nil {
		t.Error("NewPGSet(no cells): want error")
	}
}

func dbOf(p PGProvider) any {
	if p == nil {
		return nil
	}
	return p.DB()
}

func TestNewPGProvider_ThreadsPrimitives(t *testing.T) {
	tx := fakeTxRunner{}
	w := fakeWriter{}
	db := struct{ name string }{name: "pool-handle"}

	p := NewPGProvider(tx, w, db)
	if p == nil {
		t.Fatal("NewPGProvider returned nil")
	}
	if p.TxManager() == nil {
		t.Error("TxManager() must thread the injected runner (got nil)")
	}
	if p.OutboxWriter() == nil {
		t.Error("OutboxWriter() must thread the injected writer (got nil)")
	}
	if got := p.DB(); got != db {
		t.Fatalf("DB() = %v, want injected handle %v", got, db)
	}
}

func TestNewRedisProvider_ThreadsClient(t *testing.T) {
	client := struct{ name string }{name: "redis-client"}
	p := NewRedisProvider(client)
	if p == nil {
		t.Fatal("NewRedisProvider returned nil")
	}
	if got := p.Client(); got != client {
		t.Fatalf("Client() = %v, want injected client %v", got, client)
	}
}

func TestCapabilityKindConstants(t *testing.T) {
	cases := []struct {
		kind Kind
		want string
	}{
		{Postgres, "postgres"},
		{Redis, "redis"},
		{RabbitMQ, "rabbitmq"},
	}
	for _, tc := range cases {
		if string(tc.kind) != tc.want {
			t.Errorf("Kind = %q, want %q", tc.kind, tc.want)
		}
	}
}
