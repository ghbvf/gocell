// Package journal_test holds the compile-time contract pins for
// kernel/saga/journal.
//
// The var _ assertions below are the canonical source of what every journal
// implementation MUST satisfy: the full Journal, both halves of the
// JournalCore + Heartbeater split (#1209), and healthz.RepoProber. They are
// ongoing regression guards — adding a method to one of the interfaces, or a
// drifting implementation, breaks this build. Do not add build tags or
// conditional compilation to silence such an error: the broken build IS the
// test.
package journal_test

import (
	"github.com/ghbvf/gocell/framework/kernel/healthz"
	"github.com/ghbvf/gocell/framework/kernel/saga/journal"
)

// Compile-time assertions: MemJournal must satisfy the full Journal (hence both
// halves of the JournalCore + Heartbeater split, #1209) and healthz.RepoProber.
// These lines cause a compile error until MemJournal exists — that is the
// intended RED state.
var (
	_ journal.Journal     = (*journal.MemJournal)(nil)
	_ journal.JournalCore = (*journal.MemJournal)(nil)
	_ journal.Heartbeater = (*journal.MemJournal)(nil)
	_ healthz.RepoProber  = (*journal.MemJournal)(nil)
)
