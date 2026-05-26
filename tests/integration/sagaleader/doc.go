//go:build integration

// Package sagaleader is the two-process integration harness for the
// runtime/saga distlock leader-elect gate (PR-05, #964).
//
// It boots one PostgreSQL container (pgclone, the PG-TESTCONTAINER-FUNNEL-01
// sanctioned holder), applies the adapters/postgres migration set (which
// includes 040_create_saga_tables), and runs TWO runtime/saga.Coordinator
// instances against the SAME PR-04 PGJournal. Each coordinator is wired with
// WithLeaderElect over its own distlock.Locker, both Lockers sharing ONE
// locktest.FakeDriver — i.e. two "processes" contending on one shared lock
// backend (the in-memory FakeDriver enforces real key-level mutual exclusion,
// standing in for a shared Redis).
//
// The composition assertion is the issue's "同 instance 只有一个进程 advance":
// every enqueued saga instance is driven to terminal exactly once across both
// coordinators (no double Step.Run, no double commit), and the distlock path is
// exercised (SetNX / Release calls observed on the shared driver).
//
// Gate isolation (skip-when-held, fail-closed, key format, nil fail-fast) is
// covered deterministically by the white-box unit tests in
// runtime/saga/leader_elect_test.go; this package proves the composition with a
// real PG journal.
//
// Lives under tests/integration (not runtime/saga) because: (a) it imports
// adapters/postgres (cross-layer; tests/ may); (b) runtime/saga already owns an
// always-on goleak TestMain, and a second PG TestMain would collide under
// -tags=integration. A standalone package gives it its own pgclone TestMain.
package sagaleader
