package app

// Sub-command registry — the single source of truth for every
// `gocell <verb> <type>` tree (generate / verify / scaffold / check).
//
// INVARIANT: CLI-UNIMPL-HIDE-01
//
// Each verb owns one []subcommand[H] registry. Dispatch looks the type
// name up in that slice (findSub); the help surface is *derived* from the
// same slice (renderSubHelp). A type therefore cannot appear in help
// without a runnable handler, nor be runnable without a help entry — the
// two truths share one object and cannot drift. There is deliberately no
// "not implemented" / placeholder shape: an unimplemented type is simply
// absent from the registry, so it falls through to the unknown-type error
// exactly like a typo would.
//
// This makes the "no visible-but-unimplemented sub-command" property
// structurally unrepresentable rather than convention. Enforced repo-wide
// by tools/archtest/cli_unimpl_hide_test.go:
//   - upstream Hard: runGenerate/runVerify/runScaffoldWithRoot/runCheck
//     must dispatch via findSub over a subcommand[…] slice — a
//     string-literal switch/case on the type name fails archtest.
//   - downstream Hard: help must be produced by renderSubHelp from the
//     registry — a hand-written helpEntry literal carrying a string-literal
//     type name fails archtest.
//
// Funnel scope (charter §Funnel 双向锁评级): this single-source funnel
// covers ONLY the four help-bearing verb trees. The top-level `commands`
// map (dispatch.go) ↔ `PrintUsage` free-form prose is a parallel,
// NOT-yet-funneled surface of the same drift class — a new unimplemented
// top-level command could appear in PrintUsage prose without an archtest
// catching it. That gap is an explicitly registered, non-silent
// follow-up: backlog cap-14 CLI-TOPLEVEL-HELP-REGISTRY-01 (single-source
// the top-level command list the same way). Until then
// tools/archtest/cli_unimpl_hide_test.go's
// TestCLIUnimplHide01_PrintUsageNoStaleToken is the Medium compensating
// guard (no stale "indexes"/"not implemented" token in PrintUsage).
//
// ref: go-zero goctl tools/goctl — sub-commands are registered, not
// switch-dispatched; GoCell keeps the no-cobra map style but applies the
// same "help derives from the registry" single-source principle.

// subcommand is one `gocell <verb> <name>` target. H is the verb tree's
// handler signature: generate/verify/check use
// func(context.Context, []string) error; scaffold additionally needs the
// resolved project root, so it uses
// func(context.Context, string, []string) error.
type subcommand[H any] struct {
	name string
	// help is the description block rendered under name by printHelp,
	// one slice element per rendered line.
	help []string
	run  H
}

// subNames returns the registered names in declaration order. It is the
// single source for usage strings and the unknown-type error list, so no
// hand-maintained name list can drift from the registry.
func subNames[H any](subs []subcommand[H]) []string {
	names := make([]string, len(subs))
	for i, s := range subs {
		names[i] = s.name
	}
	return names
}

// findSub returns the handler registered under name. The bool is false
// when name is unregistered (caller emits the unknown-type error).
func findSub[H any](subs []subcommand[H], name string) (H, bool) {
	for _, s := range subs {
		if s.name == name {
			return s.run, true
		}
	}
	var zero H
	return zero, false
}

// renderSubHelp prints the verb's help surface derived from its registry.
// It is the only path from a subcommand registry to a helpEntry, which is
// what gives the downstream-Hard property: help text cannot list a type
// the registry does not contain.
func renderSubHelp[H any](verb string, subs []subcommand[H], footer ...string) error {
	entries := make([]helpEntry, len(subs))
	for i, s := range subs {
		entries[i] = helpEntry{name: s.name, desc: s.help}
	}
	printHelp(verb, entries, footer...)
	return nil
}
