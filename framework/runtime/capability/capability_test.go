package capability

import (
	"context"
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
)

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
