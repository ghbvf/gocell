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

// helpEntry is one type listed under a sub-command's "Types:" section. desc
// can span multiple lines; printHelp indents continuation lines under the
// type name so the help surface stays aligned.
type helpEntry struct {
	name string
	desc []string
}

// printHelp renders a uniform help surface for sub-commands. The shape is
//
//	Usage: gocell <verb> <type> [flags]
//
//	Types:
//	  <name>   <desc[0]>
//	           <desc[1]>
//	           ...
//
//	<footer>
//
// Adding a new type to a sub-command means appending a helpEntry; missing
// the help line is impossible because the data structure is the source of
// truth for the help renderer.
func printHelp(verb string, entries []helpEntry, footer ...string) {
	fmt.Printf("Usage: gocell %s <type> [flags]\n", verb)
	fmt.Println()
	fmt.Println("Types:")
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

// Per-verb help is no longer hand-written here: each verb's help surface
// is derived from its subcommand registry via renderSubHelp (see
// subcommand.go / CLI-UNIMPL-HIDE-01). printHelp + helpEntry remain as
// the shared rendering primitive renderSubHelp builds on.
