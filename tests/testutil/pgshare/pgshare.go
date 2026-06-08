//go:build integration

// Package pgshare preserves the historical shared PostgreSQL helper import path
// for consumer tests. The implementation lives in the adapter-owned pgtest
// package so root/base tests do not import this satellite helper.
package pgshare

import pgtest "github.com/ghbvf/gocell/adapters/postgres/pgtest"

type Shared = pgtest.Shared

func New(templateDB string) *Shared {
	return pgtest.New(templateDB)
}
