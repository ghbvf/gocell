package app

import (
	"fmt"
	"strings"
)

// isHelpFlag reports whether arg requests sub-command help.
//
// dispatch.go advertises `gocell <command> -h` as the discovery path; without
// this gate runGenerate/runVerify/runScaffold/runCheck would treat -h as an
// unknown sub-type because they parse args[0] before delegating to flag.Parse.
func isHelpFlag(arg string) bool {
	return arg == "-h" || arg == "--help" || arg == "help"
}

// helpEntry is one entry listed under a help surface's section ("Types:"
// for a verb tree, "Commands:" for the top level). desc can span multiple
// lines; printHelp indents continuation lines under the name so the surface
// stays aligned.
type helpEntry struct {
	name string
	desc []string
}

// printHelp renders a uniform help surface. usage is the "Usage: …" line and
// section is the list header; both are passed in so the same renderer serves
// the verb trees ("Usage: gocell <verb> <type> [flags]" / "Types:") and the
// top level ("Usage: gocell <command> [args]" / "Commands:"). The shape is
//
//	<usage>
//
//	<section>
//	  <name>   <desc[0]>
//	           <desc[1]>
//	           ...
//
//	<footer>
//
// Adding an entry means appending a helpEntry; missing the help line is
// impossible because the data structure is the source of truth for the
// renderer.
func printHelp(usage, section string, entries []helpEntry, footer ...string) {
	fmt.Println(usage)
	fmt.Println()
	fmt.Println(section)
	width := longestEntryName(entries)
	for _, e := range entries {
		first := true
		for _, line := range e.desc {
			if first {
				fmt.Printf("  %-*s  %s\n", width, e.name, line)
				first = false
				continue
			}
			fmt.Printf("  %-*s  %s\n", width, "", line)
		}
		if first {
			// entry with no description; still emit the name so the type
			// is discoverable.
			fmt.Printf("  %s\n", e.name)
		}
	}
	if len(footer) == 0 {
		return
	}
	fmt.Println()
	fmt.Println(strings.Join(footer, "\n"))
}

func longestEntryName(entries []helpEntry) int {
	max := 0
	for _, e := range entries {
		if n := len(e.name); n > max {
			max = n
		}
	}
	return max
}

// Help is no longer hand-written here: every help surface — each verb's
// "Types:" list (renderSubHelp) and the top-level "Commands:" list
// (renderTopHelp) — is derived from a subcommand registry via
// buildHelpEntries (see subcommand.go / CLI-UNIMPL-HIDE-01 +
// CLI-TOPLEVEL-HELP-REGISTRY-01). printHelp + helpEntry remain as the
// shared rendering primitive those builders feed.
