// Package persistence defines shared transaction abstractions for the
// GoCell framework. TxCtxKey is owned by the kernel so that adapters
// (e.g. adapters/postgres) can WRITE a concrete tx into ctx and cells'
// own adapter implementations can READ it, without either side importing
// the other (per CLAUDE.md cells→adapters layering rule).
//
// Contract: only ONE adapter may claim this key per assembly. If a
// second DB adapter is introduced, define its own key — do NOT reuse
// TxCtxKey for a different value type.
package persistence

import "context"

// txKey is the context key under which a database-specific transaction
// carrier (e.g. pgx.Tx) is stored. Adapters own the typed helpers;
// the key itself is kernel-owned so both adapters/ and cells/ owned
// adapters can share the key without violating layering rules.
//
// This package intentionally does not import pgx — the key is a plain
// struct value; adapters type-assert to their concrete tx type.
type txKey struct{}

// TxCtxKey is the context value key used by transactional adapters.
// Adapters (e.g. adapters/postgres) use this to store their concrete
// tx (e.g. pgx.Tx); cell-local adapters retrieve and type-assert.
//
// ref: go-zero TransactCtx — session injected via context for downstream
// participation in ambient transaction. Adopted pattern; kernel owns the
// key, adapters own the typed helpers.
var TxCtxKey = txKey{}

// TxFromContext extracts the ambient transaction carrier stored under
// [TxCtxKey], type-asserting it to T. The boolean reports whether a value of
// type T was present. The type parameter is supplied by the caller (e.g.
// adapters/postgres and cell-private PG adapters pass pgx.Tx), so this kernel
// helper stays driver-agnostic and does NOT import any DB SDK — the package
// doc invariant ("intentionally does not import pgx") is preserved while the
// extract+assert logic lives in exactly one place.
//
// Caller invariant: the writer ([CtxWithTx]) is responsible for storing a
// non-nil carrier. A typed-nil concrete pointer stored under TxCtxKey
// (e.g. (*pgx.Conn)(nil)) succeeds the type assertion and returns
// (typed-nil, true) — downstream `tx.Exec` would nil-panic. Adapters must
// only inject live transactions; this helper does not defensively re-check.
// A typed-nil interface (fakeTx(nil)) is correctly reported as (zero, false)
// since `context.WithValue` stores the underlying nil interface{}.
func TxFromContext[T any](ctx context.Context) (T, bool) {
	v, ok := ctx.Value(TxCtxKey).(T)
	return v, ok
}
