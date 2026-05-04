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

// printGenerateHelp documents the generate sub-tree, including the ownership
// boundary between gocell-generated and hand-written files. The boundary is
// enforced by writeGeneratedFile, but operators only see the enforcement as a
// terse "refusing to overwrite non-generated file" error; this surface tells
// them what they need to know up front.
func printGenerateHelp() error {
	printHelp("generate", []helpEntry{
		{"assembly", []string{
			"Generate the assembly entrypoint (cmd/<id>/main.go)",
			"and assemblies/<id>/generated/boundary.yaml.",
			"Generated files are owned by gocell. Hand-written",
			"helpers may live in cmd/<id>/run.go etc., but",
			"cmd/<id>/main.go must carry the gocell generated",
			"header or generation aborts to protect your edits.",
			"--id=<assemblyID> [--module=<module>]",
		}},
		{"metrics-schema", []string{
			"Generate assemblies/<id>/generated/metrics-schema.yaml",
			"by walking the assembly's reachable packages with",
			"go/types. --id=<assemblyID>",
		}},
		{"cell", []string{
			"Render cell_gen.go and slice_gen.go from cell.yaml /",
			"slice.yaml. <cellID> | --all [--dry-run | --verify].",
			"--verify reports drift without writing; --dry-run prints",
			"would-write file paths without writing.",
			"CI: commit cell_gen.go and run with --verify to detect stale artifacts.",
		}},
		{"indexes", []string{"(not implemented)"}},
	},
		"Generated artifacts must be committed in HEAD; gocell verify generated",
		"rejects stale or staged-only files.",
	)
	return nil
}

// printVerifyHelp mirrors printGenerateHelp for the verify sub-tree.
func printVerifyHelp() error {
	printHelp("verify", []helpEntry{
		{"slice", []string{
			"Run verify.unit + verify.contract for a slice.",
			"--id=<cellID/sliceID> [--format text|json|sarif]",
		}},
		{"cell", []string{
			"Run verify.smoke + per-slice checks for a cell.",
			"--id=<cellID> [--format text|json|sarif]",
		}},
		{"journey", []string{
			"Run a single journey or every active journey.",
			"--id=<journeyID> | --active [--format text|json|sarif]",
		}},
		{"targets", []string{
			"List slices/cells/contracts/journeys reachable from",
			"the given files. --files=<file1,file2,...>",
		}},
		{"generated", []string{
			"Verify assembly entrypoints, boundary.yaml, and",
			"metrics-schema.yaml against metadata-derived",
			"expectations and HEAD. Fails on stale, staged-only,",
			"or unexpected committed artifacts. [--module=<module>]",
		}},
	})
	return nil
}

// printScaffoldHelp documents scaffold sub-types.
func printScaffoldHelp() error {
	printHelp("scaffold", []helpEntry{
		{"cell", []string{
			"Create cells/<id>/cell.yaml. --id=<id> --team=<team>",
			"[--type=core|edge|support] [--level=L0..L4] [--dry-run]",
		}},
		{"slice", []string{
			"Create cells/<cellID>/slices/<id>/slice.yaml.",
			"--id=<id> --cell=<cellID> [--dry-run]",
		}},
		{"contract", []string{
			"Create contracts/<kind>/<domain>/<v>/contract.yaml.",
			"--id=<id> --kind=<kind> --owner=<cellID> [--dry-run]",
		}},
		{"journey", []string{
			"Create journeys/<id>.yaml.",
			"--id=<id> --goal=<goal> --team=<team> --cells=<a,b,...>",
			"[--dry-run]",
		}},
	},
		"--dry-run validates inputs and path conflicts without writing.",
	)
	return nil
}

// printCheckHelp documents check sub-types.
func printCheckHelp() error {
	printHelp("check", []helpEntry{
		{"contract-health", []string{
			"Aggregate contract metadata health.",
			"[--format text|json|sarif]",
		}},
		{"slice-coverage", []string{
			"Slice coverage of a cell.",
			"--cell=<cellID>",
		}},
		{"assembly-completeness", []string{
			"Assembly cell-set vs declared boundary.",
			"--id=<assemblyID>",
		}},
		{"journey-readiness", []string{
			"Journey readiness against status-board.",
			"--journey=<journeyID>",
		}},
		{"l0-imports", []string{
			"L0 dependency direction.",
			"--cell=<cellID>",
		}},
		{"unconditional-skip", []string{
			"Static analysis for unconditional t.Skip.",
			"[--format text|json|sarif]",
		}},
	})
	return nil
}
