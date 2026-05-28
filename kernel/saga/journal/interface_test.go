// Package journal_test is the RED-phase contract pin for kernel/saga/journal.
//
// This file intentionally does NOT compile until MemJournal is introduced in
// the GREEN batch (PR-02). The compile-time interface assertions below are the
// canonical source of what MemJournal MUST implement; any deviation will be
// caught the moment the GREEN implementation is attempted.
//
// Do not add build tags or conditional compilation to silence the error —
// the broken build IS the test.
package journal_test

import (
	"github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/kernel/saga/journal"
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
