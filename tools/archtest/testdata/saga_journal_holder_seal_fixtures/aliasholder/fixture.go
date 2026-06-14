//go:build archtest_fixture

// Package aliasholder is a RED fixture for SAGA-JOURNAL-HOLDER-SEAL-01 (F1):
// a non-Coordinator struct holds journal.JournalCore both directly and through
// a package-level type alias. The alias-typed field exercises
// classifyResolvedJournalType's types.Unalias path — on Go 1.23+ (gotypesalias=1)
// the alias materializes as *types.Alias, so without types.Unalias the seal's
// *types.Named assertion fails and the alias holder silently evades the rule.
// Loaded only via Run(t, Fixture(...)).
package aliasholder

import "github.com/ghbvf/gocell/framework/kernel/saga/journal"

// aliasedCore is a package-level alias to the Heartbeat-free core interface.
type aliasedCore = journal.JournalCore

// HolderDirect holds journal.JournalCore directly — flagged with or without the
// Unalias fix (control case proving the fixture wiring is live).
type HolderDirect struct {
	j journal.JournalCore
}

// HolderViaAlias holds the core through an alias. classifyResolvedJournalType
// must call types.Unalias to resolve it; otherwise this struct evades the seal.
type HolderViaAlias struct {
	j aliasedCore
}
