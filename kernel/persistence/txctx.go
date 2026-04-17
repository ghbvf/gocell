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
