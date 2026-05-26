package app

// Command registry — the single source of truth for every gocell command
// node, at both levels of the tree:
//   - the top-level command set (dispatch.go's `commands`):
//     `gocell <command>` — validate / scaffold / generate / check / verify /
//     graph / export;
//   - each `gocell <verb> <type>` tree (generate / verify / scaffold / check).
//
// INVARIANT:
//   - CLI-UNIMPL-HIDE-01
//   - CLI-TOPLEVEL-HELP-REGISTRY-01
//
// Each level owns one []subcommand[H] registry. Dispatch looks the name up
// in that slice (findSub); the help surface is *derived* from the same slice
// (renderTopHelp at the top level, renderSubHelp per verb), both via the
// single buildHelpEntries funnel. A command therefore cannot appear in help
// without a runnable handler, nor be runnable without a help entry — the two
// truths share one object and cannot drift. There is deliberately no
// "not implemented" / placeholder shape: an unimplemented command is simply
// absent from the registry, so it falls through to the unknown-command error
// exactly like a typo would.
//
// This makes the "no visible-but-unimplemented command" property
// structurally unrepresentable rather than convention. Enforced repo-wide
// by tools/archtest/cli_unimpl_hide_test.go:
//   - upstream: Dispatch (top level) and runGenerate/runVerify/
//     runScaffoldWithRoot/runCheck (verb trees) must dispatch via findSub
//     over a subcommand[…] slice — a string-literal switch/case on the name
//     (or a name→handler map index) fails archtest.
//   - downstream: help must be produced by buildHelpEntries from the
//     registry — a hand-written helpEntry literal carrying a string-literal
//     name fails archtest (verb trees), and PrintUsage's body must be the
//     sole renderTopHelp(commands, …) delegation (top level).
//
// The top-level `commands` ↔ `PrintUsage` surface was formerly a declared
// blind spot (free-form prose compensated by a stale-token guard); it is now
// part of the same closed-loop funnel (CLI-TOPLEVEL-HELP-REGISTRY-01).
// AI-robust grading lives in the archtest package doc
// (tools/archtest/cli_unimpl_hide_test.go), per charter "落地实例与符号清单
// 活在代码 godoc".
//
// ref: go-zero goctl tools/goctl — commands are registered, not
// switch-dispatched; GoCell keeps the no-cobra style but applies the
// same "help derives from the registry" single-source principle.

// subcommand is one gocell command node — a top-level command
// (`gocell <name>`) or a verb sub-type (`gocell <verb> <name>`). H is the
// node's handler signature: top-level commands and generate/verify/check use
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

// buildHelpEntries is the single path from a subcommand registry to
// []helpEntry. It sets `name: s.name` (a selector, never a literal), which
// is what gives the downstream property: help text cannot list a command the
// registry does not contain. Both renderSubHelp and renderTopHelp funnel
// through here, so there is exactly one helpEntry construction site in
// production (scanHelpEntryNoLiteralName bans any other).
func buildHelpEntries[H any](subs []subcommand[H]) []helpEntry {
	entries := make([]helpEntry, len(subs))
	for i, s := range subs {
		entries[i] = helpEntry{name: s.name, desc: s.help}
	}
	return entries
}

// renderSubHelp prints a verb's "Types:" help surface derived from its
// registry (`gocell <verb> -h`).
func renderSubHelp[H any](verb string, subs []subcommand[H], footer ...string) error {
	printHelp("Usage: gocell "+verb+" <type> [flags]", "Types:",
		buildHelpEntries(subs), footer...)
	return nil
}

// renderTopHelp prints the top-level "Commands:" help surface derived from
// the `commands` registry (`gocell` with no args). It is PrintUsage's sole
// statement — the downstream half of CLI-TOPLEVEL-HELP-REGISTRY-01.
func renderTopHelp[H any](subs []subcommand[H], footer ...string) {
	printHelp("Usage: gocell <command> [args]", "Commands:",
		buildHelpEntries(subs), footer...)
}
